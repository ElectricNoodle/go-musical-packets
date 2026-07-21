package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFileRepositoryReadUsesExactByteRevision(t *testing.T) {
	path := writeRepositoryConfig(t, "# retained comment\ninstance:\n  id: exact-bytes\n", 0o640)
	repository := newTestFileRepository(t, path)

	snapshot, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	sum := sha256.Sum256(contents)
	wantRevision := Revision(hex.EncodeToString(sum[:]))
	if snapshot.Revision != wantRevision {
		t.Fatalf("Read().Revision = %q, want %q", snapshot.Revision, wantRevision)
	}
	if snapshot.Config.Instance.ID != "exact-bytes" {
		t.Fatalf("Read().Config.Instance.ID = %q", snapshot.Config.Instance.ID)
	}

	secondPath := writeRepositoryConfig(t, "instance: {id: exact-bytes}\n", 0o640)
	second, err := newTestFileRepository(t, secondPath).Read()
	if err != nil {
		t.Fatalf("second Read() error = %v", err)
	}
	if second.Config.Instance.ID != snapshot.Config.Instance.ID || second.Revision == snapshot.Revision {
		t.Fatalf("semantically equal files have revisions %q and %q, want distinct exact-byte revisions", snapshot.Revision, second.Revision)
	}
}

func TestFileRepositoryReplaceWritesCanonicalFullYAMLAndPreservesMode(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: before\n", 0o640)
	repository := newTestFileRepository(t, path)
	before := mustReadRepository(t, repository)
	next := before.Config
	next.Instance.ID = "after"
	next.Rules = RulesConfig{{
		ID:      "web",
		Name:    "Web",
		Enabled: true,
		Action:  RuleActionConfig{State: FlowPlay, Channel: 2},
	}}

	change, err := repository.Replace(before.Revision, next)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if !change.Changed() || change.Before.Revision != before.Revision || change.After.Revision == before.Revision {
		t.Fatalf("Replace() change = %#v, want changed revisions", change)
	}
	if change.DurabilityWarning != nil {
		t.Fatalf("Replace().DurabilityWarning = %v", change.DurabilityWarning)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := Encode(next)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if string(contents) != string(want) {
		t.Fatalf("persisted config:\n%s\nwant canonical config:\n%s", contents, want)
	}
	for _, field := range []string{"instance:", "capture:", "mapping:", "performance:", "midi:", "server:", "peer:", "metrics:", "logging:", "rules:"} {
		if !strings.Contains(string(contents), field) {
			t.Errorf("canonical config does not contain %q", field)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("persisted mode = %o, want 640", got)
	}
	if change.After.Revision != digest(contents) {
		t.Fatalf("After.Revision = %q, want digest of persisted bytes", change.After.Revision)
	}
}

func TestFileRepositoryReplaceNoOpPreservesOriginalBytes(t *testing.T) {
	original := []byte("# deliberately sparse\ninstance: {id: unchanged}\n")
	path := writeRepositoryConfig(t, string(original), 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	renamed := false
	repository.ops.rename = func(_, _ string) error {
		renamed = true
		return errors.New("rename must not be called")
	}

	change, err := repository.Replace(snapshot.Revision, snapshot.Config)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if change.Changed() {
		t.Fatal("Replace().Changed() = true, want no-op")
	}
	if renamed {
		t.Fatal("Replace() called rename for a no-op")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != string(original) {
		t.Fatalf("no-op contents = %q, want %q", contents, original)
	}
}

func TestFileRepositoryReplaceRejectsStaleRevision(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: current\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	next := snapshot.Config
	next.Instance.ID = "next"

	_, err := repository.Replace(Revision("stale"), next)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Replace() error = %v, want *ConflictError", err)
	}
	if conflict.Expected != "stale" || conflict.Actual != snapshot.Revision {
		t.Fatalf("ConflictError = %#v", conflict)
	}
}

func TestFileRepositoryReplaceRejectsInvalidConfigBeforeCreatingTemp(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: current\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	invalid := snapshot.Config
	invalid.Instance.ID = ""
	created := false
	repository.ops.createTemp = func(string, string) (*os.File, error) {
		created = true
		return nil, errors.New("unexpected temp creation")
	}

	if _, err := repository.Replace(snapshot.Revision, invalid); err == nil || !strings.Contains(err.Error(), "instance.id") {
		t.Fatalf("Replace() error = %v, want validation error", err)
	}
	if created {
		t.Fatal("Replace() created a temporary file for invalid configuration")
	}
}

func TestFileRepositoryConcurrentReplaceAllowsOneRevisionWinner(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: initial\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	start := make(chan struct{})
	errorsByWriter := make([]error, 2)
	var writers sync.WaitGroup
	for index, id := range []string{"first", "second"} {
		writers.Add(1)
		go func(index int, id string) {
			defer writers.Done()
			<-start
			next := snapshot.Config
			next.Instance.ID = id
			_, errorsByWriter[index] = repository.Replace(snapshot.Revision, next)
		}(index, id)
	}
	close(start)
	writers.Wait()

	var successes, conflicts int
	for _, err := range errorsByWriter {
		if err == nil {
			successes++
			continue
		}
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("concurrent Replace() error = %v, want nil or *ConflictError", err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = %d successes, %d conflicts; want one each", successes, conflicts)
	}
	current := mustReadRepository(t, repository)
	if current.Config.Instance.ID != "first" && current.Config.Instance.ID != "second" {
		t.Fatalf("current instance ID = %q, want one winning candidate", current.Config.Instance.ID)
	}
}

func TestFileRepositoryReplaceRechecksRevisionImmediatelyBeforeRename(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: initial\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	next := snapshot.Config
	next.Instance.ID = "proposed"
	external := []byte("instance:\n  id: external\n")
	originalLstat := repository.ops.lstat
	reads := 0
	repository.ops.lstat = func(name string) (fs.FileInfo, error) {
		reads++
		if reads == 2 {
			if err := os.WriteFile(path, external, 0o600); err != nil {
				t.Fatalf("external WriteFile() error = %v", err)
			}
		}
		return originalLstat(name)
	}

	_, err := repository.Replace(snapshot.Revision, next)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Replace() error = %v, want final *ConflictError", err)
	}
	if conflict.Actual != digest(external) {
		t.Fatalf("ConflictError.Actual = %q, want external revision %q", conflict.Actual, digest(external))
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(contents) != string(external) {
		t.Fatalf("contents = %q, want external update %q", contents, external)
	}
	assertNoRepositoryTemps(t, path)
}

func TestFileRepositoryReplaceDoesNotOverwriteConcurrentModeChange(t *testing.T) {
	original := []byte("instance:\n  id: initial\n")
	path := writeRepositoryConfig(t, string(original), 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	next := snapshot.Config
	next.Instance.ID = "proposed"
	originalLstat := repository.ops.lstat
	reads := 0
	repository.ops.lstat = func(name string) (fs.FileInfo, error) {
		reads++
		if reads == 2 {
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatalf("external Chmod() error = %v", err)
			}
		}
		return originalLstat(name)
	}

	_, err := repository.Replace(snapshot.Revision, next)
	if err == nil || !strings.Contains(err.Error(), "mode changed") {
		t.Fatalf("Replace() error = %v, want mode race rejection", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(contents) != string(original) {
		t.Fatalf("contents = %q, want unchanged %q", contents, original)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want concurrent mode 640", got)
	}
	assertNoRepositoryTemps(t, path)
}

func TestFileRepositoryReplaceFailuresBeforeRenameLeaveOriginal(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*FileRepository, error)
	}{
		{
			name: "create temp",
			inject: func(repository *FileRepository, failure error) {
				repository.ops.createTemp = func(string, string) (*os.File, error) { return nil, failure }
			},
		},
		{
			name: "write",
			inject: func(repository *FileRepository, failure error) {
				repository.ops.write = func(*os.File, []byte) error { return failure }
			},
		},
		{
			name: "chmod",
			inject: func(repository *FileRepository, failure error) {
				repository.ops.chmod = func(*os.File, fs.FileMode) error { return failure }
			},
		},
		{
			name: "file sync",
			inject: func(repository *FileRepository, failure error) {
				repository.ops.sync = func(*os.File) error { return failure }
			},
		},
		{
			name: "file close",
			inject: func(repository *FileRepository, failure error) {
				originalClose := repository.ops.close
				repository.ops.close = func(file *os.File) error {
					if strings.Contains(filepath.Base(file.Name()), ".tmp-") {
						_ = originalClose(file)
						return failure
					}
					return originalClose(file)
				}
			},
		},
		{
			name: "rename",
			inject: func(repository *FileRepository, failure error) {
				repository.ops.rename = func(string, string) error { return failure }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := []byte("instance:\n  id: original\n")
			path := writeRepositoryConfig(t, string(original), 0o640)
			repository := newTestFileRepository(t, path)
			snapshot := mustReadRepository(t, repository)
			next := snapshot.Config
			next.Instance.ID = "next"
			failure := errors.New("injected " + test.name + " failure")
			test.inject(repository, failure)

			_, err := repository.Replace(snapshot.Revision, next)
			if !errors.Is(err, failure) {
				t.Fatalf("Replace() error = %v, want injected failure", err)
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			if string(contents) != string(original) {
				t.Fatalf("contents after failure = %q, want %q", contents, original)
			}
			assertNoRepositoryTemps(t, path)
		})
	}
}

func TestFileRepositoryDirectorySyncFailureReturnsCommittedChange(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: before\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	next := snapshot.Config
	next.Instance.ID = "committed"
	failure := errors.New("injected directory sync failure")
	originalSync := repository.ops.sync
	renameCompleted := false
	originalRename := repository.ops.rename
	repository.ops.rename = func(oldPath, newPath string) error {
		if err := originalRename(oldPath, newPath); err != nil {
			return err
		}
		renameCompleted = true
		return nil
	}
	repository.ops.sync = func(file *os.File) error {
		if file.Name() == filepath.Dir(repository.path) {
			if !renameCompleted {
				t.Fatal("directory sync occurred before rename commit point")
			}
			return failure
		}
		return originalSync(file)
	}

	change, err := repository.Replace(snapshot.Revision, next)
	if err != nil {
		t.Fatalf("Replace() error = %v, want committed change", err)
	}
	if !change.Changed() || !errors.Is(change.DurabilityWarning, failure) {
		t.Fatalf("Replace() change = %#v, warning = %v", change, change.DurabilityWarning)
	}
	current := mustReadRepository(t, repository)
	if current.Revision != change.After.Revision || current.Config.Instance.ID != "committed" {
		t.Fatalf("current snapshot = %#v, want committed change %#v", current, change.After)
	}
}

func TestFileRepositorySyncsFileAndDirectoryOnSuccess(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: before\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	next := snapshot.Config
	next.Instance.ID = "after"
	originalSync := repository.ops.sync
	originalRename := repository.ops.rename
	fileSyncs := 0
	directorySyncs := 0
	renameCompleted := false
	repository.ops.rename = func(oldPath, newPath string) error {
		if err := originalRename(oldPath, newPath); err != nil {
			return err
		}
		renameCompleted = true
		return nil
	}
	repository.ops.sync = func(file *os.File) error {
		if file.Name() == filepath.Dir(repository.path) {
			directorySyncs++
			if !renameCompleted {
				t.Fatal("directory sync occurred before rename commit point")
			}
		} else if strings.Contains(filepath.Base(file.Name()), ".tmp-") {
			fileSyncs++
			if renameCompleted {
				t.Fatal("temporary file sync occurred after rename")
			}
		}
		return originalSync(file)
	}

	change, err := repository.Replace(snapshot.Revision, next)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if change.DurabilityWarning != nil {
		t.Fatalf("DurabilityWarning = %v", change.DurabilityWarning)
	}
	if fileSyncs != 1 || directorySyncs != 1 {
		t.Fatalf("sync counts = file %d, directory %d; want one each", fileSyncs, directorySyncs)
	}
}

func TestFileRepositorySerializesRepositoriesWithCanonicalLockPath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("instance:\n  id: before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	linkDirectory := filepath.Join(t.TempDir(), "linked-parent")
	if err := os.Symlink(directory, linkDirectory); err != nil {
		t.Skipf("Symlink() error = %v", err)
	}

	firstRepository := newTestFileRepository(t, path)
	secondRepository := newTestFileRepository(t, filepath.Join(linkDirectory, "config.yaml"))
	if firstRepository.path != secondRepository.path || firstRepository.lockPath != secondRepository.lockPath {
		t.Fatalf(
			"canonical paths differ: first (%q, %q), second (%q, %q)",
			firstRepository.path,
			firstRepository.lockPath,
			secondRepository.path,
			secondRepository.lockPath,
		)
	}
	snapshot := mustReadRepository(t, firstRepository)
	firstConfig := snapshot.Config
	firstConfig.Instance.ID = "first"
	secondConfig := snapshot.Config
	secondConfig.Instance.ID = "second"

	enteredCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	originalCreateTemp := firstRepository.ops.createTemp
	firstRepository.ops.createTemp = func(directory, pattern string) (*os.File, error) {
		close(enteredCommit)
		<-releaseCommit
		return originalCreateTemp(directory, pattern)
	}
	type replaceResult struct {
		change Change
		err    error
	}
	firstResult := make(chan replaceResult, 1)
	go func() {
		change, err := firstRepository.Replace(snapshot.Revision, firstConfig)
		firstResult <- replaceResult{change: change, err: err}
	}()
	<-enteredCommit

	// A nonblocking acquisition from the second repository must observe the
	// first repository's held lock before either replacement can rename.
	probe, err := secondRepository.ops.openLock(secondRepository.lockPath)
	if err != nil {
		t.Fatalf("open lock probe error = %v", err)
	}
	if err := secondRepository.ops.flock(probe, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		_ = secondRepository.ops.close(probe)
		close(releaseCommit)
		t.Fatalf("nonblocking lock error = %v, want EWOULDBLOCK", err)
	}
	if err := secondRepository.ops.close(probe); err != nil {
		t.Fatalf("close lock probe error = %v", err)
	}

	secondResult := make(chan replaceResult, 1)
	go func() {
		change, err := secondRepository.Replace(snapshot.Revision, secondConfig)
		secondResult <- replaceResult{change: change, err: err}
	}()
	close(releaseCommit)
	results := []replaceResult{<-firstResult, <-secondResult}

	successes := 0
	conflicts := 0
	for _, result := range results {
		switch {
		case result.err == nil && result.change.Changed():
			successes++
		default:
			var conflict *ConflictError
			if errors.As(result.err, &conflict) {
				conflicts++
			} else {
				t.Fatalf("Replace() result = change %#v, error %v", result.change, result.err)
			}
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("Replace() results = %d successes, %d conflicts; want one each", successes, conflicts)
	}
	if info, err := os.Stat(firstRepository.lockPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("persistent lock artifact Stat() = (%v, %v), want regular file", info, err)
	}
}

func TestFileRepositoryReadDoesNotRequireAdvisoryLock(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: readable\n", 0o600)
	repository := newTestFileRepository(t, path)
	called := false
	repository.ops.openLock = func(string) (*os.File, error) {
		called = true
		return nil, errors.New("lock unavailable")
	}

	snapshot, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if called {
		t.Fatal("Read() attempted to open the advisory lock")
	}
	if snapshot.Config.Instance.ID != "readable" {
		t.Fatalf("Read().Config.Instance.ID = %q", snapshot.Config.Instance.ID)
	}
}

func TestFileRepositoryPostCommitLockReleaseFailureIsWarning(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*FileRepository, error)
	}{
		{
			name: "unlock",
			inject: func(repository *FileRepository, failure error) {
				originalFlock := repository.ops.flock
				repository.ops.flock = func(file *os.File, operation int) error {
					if operation == unix.LOCK_UN {
						_ = originalFlock(file, operation)
						return failure
					}
					return originalFlock(file, operation)
				}
			},
		},
		{
			name: "close",
			inject: func(repository *FileRepository, failure error) {
				originalClose := repository.ops.close
				repository.ops.close = func(file *os.File) error {
					err := originalClose(file)
					if file.Name() == repository.lockPath {
						return errors.Join(err, failure)
					}
					return err
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeRepositoryConfig(t, "instance:\n  id: before\n", 0o600)
			repository := newTestFileRepository(t, path)
			snapshot := mustReadRepository(t, repository)
			next := snapshot.Config
			next.Instance.ID = "committed"
			failure := errors.New("injected lock " + test.name + " failure")
			test.inject(repository, failure)

			change, err := repository.Replace(snapshot.Revision, next)
			if err != nil {
				t.Fatalf("Replace() error = %v, want committed change", err)
			}
			if !change.Changed() || !errors.Is(change.DurabilityWarning, failure) {
				t.Fatalf("Replace() = %#v, warning %v; want committed warning", change, change.DurabilityWarning)
			}
			contents, err := os.ReadFile(repository.path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if digest(contents) != change.After.Revision {
				t.Fatalf("persisted revision = %q, want %q", digest(contents), change.After.Revision)
			}
		})
	}
}

func TestFileRepositoryRollbackRejectsModeOnlyChangeAfterCommit(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: before\n", 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	next := snapshot.Config
	next.Instance.ID = "after"
	change, err := repository.Replace(snapshot.Revision, next)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if err := os.Chmod(repository.path, 0o640); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if _, err := repository.Rollback(change); err == nil || !strings.Contains(err.Error(), "mode changed after commit") {
		t.Fatalf("Rollback() error = %v, want mode-only conflict", err)
	}
	current := mustReadRepository(t, repository)
	if current.Revision != change.After.Revision || current.Config.Instance.ID != "after" {
		t.Fatalf("current snapshot = %#v, want committed config unchanged", current)
	}
	info, err := os.Stat(repository.path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want external mode 640", info.Mode().Perm())
	}
}

func TestFileRepositoryBoundsReadsAndReplacements(t *testing.T) {
	t.Run("read overflow", func(t *testing.T) {
		contents := "#" + strings.Repeat("x", maximumConfigBytes) + "\n"
		path := writeRepositoryConfig(t, contents, 0o600)
		repository := newTestFileRepository(t, path)
		if _, err := repository.Read(); err == nil || !strings.Contains(err.Error(), "maximum is 1048576 bytes") {
			t.Fatalf("Read() error = %v, want size bound", err)
		}
	})

	t.Run("replacement overflow", func(t *testing.T) {
		original := []byte("instance:\n  id: before\n")
		path := writeRepositoryConfig(t, string(original), 0o600)
		repository := newTestFileRepository(t, path)
		snapshot := mustReadRepository(t, repository)
		next := snapshot.Config
		next.Instance.ID = strings.Repeat("x", maximumConfigBytes)
		createdTemp := false
		repository.ops.createTemp = func(string, string) (*os.File, error) {
			createdTemp = true
			return nil, errors.New("temporary file must not be created")
		}

		if _, err := repository.Replace(snapshot.Revision, next); err == nil || !strings.Contains(err.Error(), "maximum is 1048576 bytes") {
			t.Fatalf("Replace() error = %v, want size bound", err)
		}
		if createdTemp {
			t.Fatal("Replace() created a temporary file for oversized content")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(contents) != string(original) {
			t.Fatalf("contents = %q, want unchanged original", contents)
		}
	})
}

func TestFileRepositoryNoOpChangeSnapshotsDoNotAlias(t *testing.T) {
	configuration := Default()
	configuration.Instance.ID = "independent"
	configuration.Rules = RulesConfig{{
		ID:      "rule",
		Enabled: true,
		Match: RuleMatchConfig{
			SourcePorts:      &PortRangeConfig{Minimum: 80, Maximum: 80},
			DestinationPorts: &PortRangeConfig{Minimum: 443, Maximum: 443},
			WireSize:         &SizeRangeConfig{Minimum: 64, Maximum: 1500},
			RequiredTCPFlags: []TCPFlag{TCPFlagSYN, TCPFlagACK},
		},
		Action: RuleActionConfig{State: FlowPlay, Channel: 1},
	}}
	encoded, err := Encode(configuration)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	path := writeRepositoryConfig(t, string(encoded), 0o600)
	repository := newTestFileRepository(t, path)
	snapshot := mustReadRepository(t, repository)
	change, err := repository.Replace(snapshot.Revision, snapshot.Config)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if change.Changed() {
		t.Fatal("Replace().Changed() = true, want no-op")
	}

	change.Before.Config.Rules[0].ID = "mutated"
	change.Before.Config.Rules[0].Match.SourcePorts.Minimum = 1
	change.Before.Config.Rules[0].Match.DestinationPorts.Minimum = 2
	change.Before.Config.Rules[0].Match.WireSize.Minimum = 3
	change.Before.Config.Rules[0].Match.RequiredTCPFlags[0] = TCPFlagRST
	afterRule := change.After.Config.Rules[0]
	if afterRule.ID != "rule" ||
		afterRule.Match.SourcePorts.Minimum != 80 ||
		afterRule.Match.DestinationPorts.Minimum != 443 ||
		afterRule.Match.WireSize.Minimum != 64 ||
		afterRule.Match.RequiredTCPFlags[0] != TCPFlagSYN {
		t.Fatalf("After snapshot was mutated through Before alias: %#v", afterRule)
	}
	if snapshot.Config.Rules[0].ID != "rule" || snapshot.Config.Rules[0].Match.SourcePorts.Minimum != 80 {
		t.Fatalf("source snapshot was mutated through change alias: %#v", snapshot.Config.Rules[0])
	}
}

func TestFileRepositoryRollbackRestoresExactBytesAndMode(t *testing.T) {
	original := []byte("# preserve me\ninstance: {id: before}\n")
	path := writeRepositoryConfig(t, string(original), 0o604)
	repository := newTestFileRepository(t, path)
	before := mustReadRepository(t, repository)
	next := before.Config
	next.Instance.ID = "after"
	change, err := repository.Replace(before.Revision, next)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	canonicalAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	// Exported reporting fields are not trusted for rollback authority or data.
	change.Before.Config.Instance.ID = "tampered"
	change.Before.Revision = "tampered"
	change.After.Revision = "tampered"
	rollback, err := repository.Rollback(change)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if !rollback.Changed() || rollback.After.Revision != before.Revision {
		t.Fatalf("Rollback() = %#v, want original revision %q", rollback, before.Revision)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != string(original) {
		t.Fatalf("rollback contents = %q, want exact original %q", contents, original)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o604 {
		t.Fatalf("rollback mode = %o, want 604", got)
	}

	// A rollback itself is a committed change and can be rolled back safely.
	redo, err := repository.Rollback(rollback)
	if err != nil {
		t.Fatalf("Rollback(rollback) error = %v", err)
	}
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != string(canonicalAfter) || redo.After.Revision != digest(canonicalAfter) {
		t.Fatalf("redo contents/revision do not restore replacement")
	}
}

func TestFileRepositoryRollbackUsesCASAndRepositoryProvenance(t *testing.T) {
	path := writeRepositoryConfig(t, "instance:\n  id: before\n", 0o600)
	repository := newTestFileRepository(t, path)
	before := mustReadRepository(t, repository)
	firstConfig := before.Config
	firstConfig.Instance.ID = "first"
	first, err := repository.Replace(before.Revision, firstConfig)
	if err != nil {
		t.Fatalf("first Replace() error = %v", err)
	}

	otherRepository := newTestFileRepository(t, path)
	if _, err := otherRepository.Rollback(first); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign Rollback() error = %v, want provenance rejection", err)
	}

	secondConfig := first.After.Config
	secondConfig.Instance.ID = "second"
	second, err := repository.Replace(first.After.Revision, secondConfig)
	if err != nil {
		t.Fatalf("second Replace() error = %v", err)
	}
	_, err = repository.Rollback(first)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale Rollback() error = %v, want *ConflictError", err)
	}
	if conflict.Expected != first.After.Revision || conflict.Actual != second.After.Revision {
		t.Fatalf("rollback ConflictError = %#v", conflict)
	}
}

func TestFileRepositoryRejectsMissingSymlinkAndNonRegularPaths(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.yaml")
		repository := newTestFileRepository(t, path)
		if _, err := repository.Read(); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read() error = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		repository := newTestFileRepository(t, t.TempDir())
		if _, err := repository.Read(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Read() error = %v, want non-regular rejection", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		target := writeRepositoryConfig(t, "instance:\n  id: target\n", 0o600)
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("Symlink() error = %v", err)
		}
		repository := newTestFileRepository(t, path)
		if _, err := repository.Read(); err == nil || !strings.Contains(err.Error(), "symbolic links") {
			t.Fatalf("Read() error = %v, want symlink rejection", err)
		}
	})
}

func TestNewFileRepositoryRejectsBlankPath(t *testing.T) {
	if _, err := NewFileRepository(" \t"); err == nil {
		t.Fatal("NewFileRepository() error = nil, want blank path rejection")
	}
}

func TestWritableModeStripsSpecialBits(t *testing.T) {
	if got := writableMode(os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o754); got != 0o754 {
		t.Fatalf("writableMode() = %v, want permission bits only", got)
	}
}

func writeRepositoryConfig(t *testing.T, contents string, mode fs.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	return path
}

func newTestFileRepository(t *testing.T, path string) *FileRepository {
	t.Helper()
	repository, err := NewFileRepository(path)
	if err != nil {
		t.Fatalf("NewFileRepository() error = %v", err)
	}
	return repository
}

func mustReadRepository(t *testing.T, repository *FileRepository) Snapshot {
	t.Helper()
	snapshot, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	return snapshot
}

func assertNoRepositoryTemps(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}
