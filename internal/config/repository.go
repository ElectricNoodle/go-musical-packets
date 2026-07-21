package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const maximumConfigBytes = 1 << 20

// Revision is the SHA-256 digest of the exact bytes stored in a configuration
// file. It is suitable for optimistic concurrency checks.
type Revision string

func (revision Revision) String() string {
	return string(revision)
}

// RevisionOf returns the exact-byte revision for contents.
func RevisionOf(contents []byte) Revision {
	return digest(contents)
}

// Snapshot is a validated view of a configuration file at one revision.
// Values returned by a repository retain private data needed to perform an
// exact rollback; constructing a Snapshot directly does not confer that
// capability.
type Snapshot struct {
	Config   Config   `json:"config"`
	Revision Revision `json:"revision"`

	state *snapshotState
}

// Change describes a replacement. A durability warning means the rename
// committed the change, but a post-commit directory-sync or lock-release step
// reported a problem. The change must still be treated as committed.
type Change struct {
	Before            Snapshot `json:"before"`
	After             Snapshot `json:"after"`
	DurabilityWarning error    `json:"-"`

	state *changeState
}

// Changed reports whether the replacement changed either the file bytes or
// its preserved mode.
func (change Change) Changed() bool {
	if change.state != nil {
		return change.state.changed
	}
	return change.Before.Revision != change.After.Revision
}

// ConflictError reports that the file no longer has the expected revision.
type ConflictError struct {
	Expected Revision
	Actual   Revision
}

func (err *ConflictError) Error() string {
	return fmt.Sprintf("configuration revision conflict: expected %s, found %s", err.Expected, err.Actual)
}

// FileRepository applies compare-and-swap configuration changes to one file.
// It must not be copied after first use.
//
// Cooperating processes are serialized through a persistent
// .<config-name>.lock file beside the configuration. The lock artifact is
// deliberately not removed, because unlinking it could let processes lock
// different inodes. Advisory locking cannot prevent writes by programs that do
// not use the same lock.
type FileRepository struct {
	noCopy     noCopy
	path       string
	lockPath   string
	provenance *repositoryProvenance
	ops        fileRepositoryOps
	mu         sync.Mutex
}

// NewFileRepository constructs a repository for path. File type and existence
// are checked by each operation so changes outside the process are detected.
func NewFileRepository(path string) (*FileRepository, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config repository path is required")
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config repository path %q: %w", path, err)
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, fmt.Errorf("resolve config repository parent %q: %w", filepath.Dir(absolute), err)
	}
	base := filepath.Base(absolute)
	return &FileRepository{
		path:       filepath.Join(canonicalParent, base),
		lockPath:   filepath.Join(canonicalParent, "."+base+".lock"),
		provenance: &repositoryProvenance{},
		ops:        defaultFileRepositoryOps(),
	}, nil
}

// Read returns the current validated configuration and its exact-byte
// revision.
func (repository *FileRepository) Read() (Snapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	return repository.read()
}

// Replace validates configuration and atomically installs its canonical full
// YAML encoding when expected still identifies the current file. Replacing a
// semantically identical configuration is a no-op and preserves the original
// bytes and revision. Replacements preserve only permission bits; ownership,
// ACLs, extended attributes, and other filesystem metadata are not preserved
// by the atomic rename.
func (repository *FileRepository) Replace(expected Revision, configuration Config) (change Change, resultErr error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	lock, err := repository.acquireLock(unix.LOCK_EX)
	if err != nil {
		return Change{}, err
	}
	defer func() {
		repository.finishMutation(&change, &resultErr, lock)
	}()

	current, err := repository.read()
	if err != nil {
		return Change{}, err
	}
	if err := checkRevision(expected, current.Revision); err != nil {
		return Change{}, err
	}

	contents, err := Encode(configuration)
	if err != nil {
		return Change{}, err
	}
	if len(contents) > maximumConfigBytes {
		return Change{}, configSizeError(len(contents))
	}
	currentCanonical, err := Encode(current.Config)
	if err != nil {
		return Change{}, fmt.Errorf("encode current config %q: %w", repository.path, err)
	}
	if bytes.Equal(contents, currentCanonical) {
		return repository.change(current, current, nil), nil
	}

	target, err := repository.snapshot(contents, current.state.mode)
	if err != nil {
		return Change{}, fmt.Errorf("prepare replacement config %q: %w", repository.path, err)
	}
	return repository.commit(current, expected, target)
}

// Rollback restores the exact bytes and mode replaced by change. It succeeds
// only when change was produced by this repository and its After revision is
// still current, so a rollback cannot overwrite a later update.
// Rollback also restores only the prior permission bits. It cannot restore
// ownership, ACLs, extended attributes, or other metadata discarded by a
// replacement.
func (repository *FileRepository) Rollback(change Change) (rollback Change, resultErr error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	lock, err := repository.acquireLock(unix.LOCK_EX)
	if err != nil {
		return Change{}, err
	}
	defer func() {
		repository.finishMutation(&rollback, &resultErr, lock)
	}()

	if change.state == nil || change.state.provenance != repository.provenance {
		return Change{}, errors.New("rollback change does not belong to this config repository")
	}

	current, err := repository.read()
	if err != nil {
		return Change{}, err
	}
	if err := checkRevision(change.state.afterRevision, current.Revision); err != nil {
		return Change{}, err
	}
	if current.state.mode != change.state.afterMode {
		return Change{}, fmt.Errorf(
			"config file mode changed after commit: expected %04o, found %04o",
			change.state.afterMode.Perm(),
			current.state.mode.Perm(),
		)
	}

	target, err := repository.snapshot(change.state.beforeContents, change.state.beforeMode)
	if err != nil {
		return Change{}, fmt.Errorf("prepare config rollback %q: %w", repository.path, err)
	}
	if bytes.Equal(current.state.contents, target.state.contents) && current.state.mode == target.state.mode {
		return repository.change(current, current, nil), nil
	}
	return repository.commit(current, change.state.afterRevision, target)
}

func (repository *FileRepository) commit(before Snapshot, expected Revision, target Snapshot) (change Change, resultErr error) {
	directory := filepath.Dir(repository.path)
	pattern := "." + filepath.Base(repository.path) + ".tmp-*"
	temporary, err := repository.ops.createTemp(directory, pattern)
	if err != nil {
		return Change{}, fmt.Errorf("create temporary config beside %q: %w", repository.path, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			closed = true
			if err := repository.ops.close(temporary); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close temporary config %q: %w", temporaryPath, err))
			}
		}
		if !committed {
			if err := repository.ops.remove(temporaryPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary config %q: %w", temporaryPath, err))
			}
		}
	}()

	if err := repository.ops.write(temporary, target.state.contents); err != nil {
		return Change{}, fmt.Errorf("write temporary config %q: %w", temporaryPath, err)
	}
	if err := repository.ops.chmod(temporary, target.state.mode); err != nil {
		return Change{}, fmt.Errorf("set temporary config mode %q: %w", temporaryPath, err)
	}
	if err := repository.ops.sync(temporary); err != nil {
		return Change{}, fmt.Errorf("sync temporary config %q: %w", temporaryPath, err)
	}
	closed = true
	if err := repository.ops.close(temporary); err != nil {
		return Change{}, fmt.Errorf("close temporary config %q: %w", temporaryPath, err)
	}

	latest, err := repository.read()
	if err != nil {
		return Change{}, fmt.Errorf("recheck config before replace: %w", err)
	}
	if err := checkRevision(expected, latest.Revision); err != nil {
		return Change{}, err
	}
	if latest.state.mode != before.state.mode {
		return Change{}, fmt.Errorf(
			"config file mode changed before replace: was %04o, now %04o",
			before.state.mode.Perm(),
			latest.state.mode.Perm(),
		)
	}

	if err := repository.ops.rename(temporaryPath, repository.path); err != nil {
		return Change{}, fmt.Errorf("replace config %q: %w", repository.path, err)
	}
	committed = true

	change = repository.change(latest, target, repository.syncDirectory())
	return change, nil
}

func (repository *FileRepository) read() (Snapshot, error) {
	info, err := repository.ops.lstat(repository.path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect config %q: %w", repository.path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, fmt.Errorf("inspect config %q: symbolic links are not supported", repository.path)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("inspect config %q: not a regular file", repository.path)
	}

	file, err := repository.ops.open(repository.path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open config %q: %w", repository.path, err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maximumConfigBytes+1))
	openedInfo, statErr := file.Stat()
	closeErr := repository.ops.close(file)
	if err := errors.Join(readErr, statErr, closeErr); err != nil {
		return Snapshot{}, fmt.Errorf("read config %q: %w", repository.path, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return Snapshot{}, fmt.Errorf("inspect config %q: file changed while opening", repository.path)
	}
	if len(contents) > maximumConfigBytes {
		return Snapshot{}, configSizeError(len(contents))
	}

	snapshot, err := repository.snapshot(contents, openedInfo.Mode())
	if err != nil {
		return Snapshot{}, fmt.Errorf("load config %q: %w", repository.path, err)
	}
	return snapshot, nil
}

func (repository *FileRepository) snapshot(contents []byte, mode fs.FileMode) (Snapshot, error) {
	if len(contents) > maximumConfigBytes {
		return Snapshot{}, configSizeError(len(contents))
	}
	configuration, err := Decode(bytes.NewReader(contents))
	if err != nil {
		return Snapshot{}, err
	}
	contents = bytes.Clone(contents)
	state := &snapshotState{
		contents:   contents,
		mode:       writableMode(mode),
		provenance: repository.provenance,
	}
	return Snapshot{
		Config:   configuration,
		Revision: digest(contents),
		state:    state,
	}, nil
}

func (repository *FileRepository) change(before, after Snapshot, warning error) Change {
	changed := before.Revision != after.Revision || before.state.mode != after.state.mode
	return Change{
		Before:            cloneSnapshot(before),
		After:             cloneSnapshot(after),
		DurabilityWarning: warning,
		state: &changeState{
			provenance:     repository.provenance,
			beforeContents: bytes.Clone(before.state.contents),
			beforeMode:     before.state.mode,
			afterRevision:  after.Revision,
			afterMode:      after.state.mode,
			changed:        changed,
		},
	}
}

func (repository *FileRepository) acquireLock(operation int) (*os.File, error) {
	lock, err := repository.ops.openLock(repository.lockPath)
	if err != nil {
		return nil, fmt.Errorf("open config lock %q: %w", repository.lockPath, err)
	}
	if err := repository.ops.flock(lock, operation); err != nil {
		closeErr := repository.ops.close(lock)
		return nil, errors.Join(
			fmt.Errorf("acquire config lock %q: %w", repository.lockPath, err),
			closeErr,
		)
	}
	return lock, nil
}

func (repository *FileRepository) releaseLock(lock *os.File) error {
	unlockErr := repository.ops.flock(lock, unix.LOCK_UN)
	closeErr := repository.ops.close(lock)
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("release config lock %q: %w", repository.lockPath, err)
	}
	return nil
}

func (repository *FileRepository) finishMutation(change *Change, resultErr *error, lock *os.File) {
	releaseErr := repository.releaseLock(lock)
	if releaseErr == nil {
		return
	}
	if *resultErr == nil && change.Changed() {
		change.DurabilityWarning = errors.Join(change.DurabilityWarning, releaseErr)
		return
	}
	*resultErr = errors.Join(*resultErr, releaseErr)
}

func (repository *FileRepository) syncDirectory() error {
	directoryPath := filepath.Dir(repository.path)
	directory, err := repository.ops.open(directoryPath)
	if err != nil {
		return fmt.Errorf("open config directory %q after commit: %w", directoryPath, err)
	}
	syncErr := repository.ops.sync(directory)
	closeErr := repository.ops.close(directory)
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync config directory %q after commit: %w", directoryPath, err)
	}
	return nil
}

func checkRevision(expected, actual Revision) error {
	if expected == actual {
		return nil
	}
	return &ConflictError{Expected: expected, Actual: actual}
}

func digest(contents []byte) Revision {
	sum := sha256.Sum256(contents)
	return Revision(hex.EncodeToString(sum[:]))
}

func writableMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm()
}

func configSizeError(size int) error {
	return fmt.Errorf("config is %d bytes; maximum is %d bytes", size, maximumConfigBytes)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Config = snapshot.Config.Clone()
	if snapshot.state != nil {
		state := *snapshot.state
		state.contents = bytes.Clone(snapshot.state.contents)
		clone.state = &state
	}
	return clone
}

type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// repositoryProvenance must not be zero-sized: Go permits distinct pointers to
// zero-sized variables to compare equal.
type repositoryProvenance struct {
	identity byte
}

type snapshotState struct {
	contents   []byte
	mode       fs.FileMode
	provenance *repositoryProvenance
}

type changeState struct {
	provenance     *repositoryProvenance
	beforeContents []byte
	beforeMode     fs.FileMode
	afterRevision  Revision
	afterMode      fs.FileMode
	changed        bool
}

type fileRepositoryOps struct {
	lstat      func(string) (fs.FileInfo, error)
	open       func(string) (*os.File, error)
	openLock   func(string) (*os.File, error)
	flock      func(*os.File, int) error
	createTemp func(string, string) (*os.File, error)
	write      func(*os.File, []byte) error
	chmod      func(*os.File, fs.FileMode) error
	sync       func(*os.File) error
	close      func(*os.File) error
	rename     func(string, string) error
	remove     func(string) error
}

func defaultFileRepositoryOps() fileRepositoryOps {
	return fileRepositoryOps{
		lstat:      os.Lstat,
		open:       os.Open,
		openLock:   openRepositoryLock,
		flock:      func(file *os.File, operation int) error { return unix.Flock(int(file.Fd()), operation) },
		createTemp: os.CreateTemp,
		write: func(file *os.File, contents []byte) error {
			written, err := file.Write(contents)
			if err == nil && written != len(contents) {
				return io.ErrShortWrite
			}
			return err
		},
		chmod:  func(file *os.File, mode fs.FileMode) error { return file.Chmod(mode) },
		sync:   func(file *os.File) error { return file.Sync() },
		close:  func(file *os.File) error { return file.Close() },
		rename: os.Rename,
		remove: os.Remove,
	}
}

func openRepositoryLock(path string) (*os.File, error) {
	descriptor, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("create config lock file handle")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("config lock is not a regular file")
	}
	return file, nil
}
