package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/peer"
)

const (
	maximumKnownManagementRevisions = 256
	managementSecretExpansionBudget = 64 << 10
)

type managementBackend struct {
	controller *Controller
	registry   *flow.Registry
	interfaces func() ([]capture.Interface, error)
	midi       managementMIDI
	ready      *atomic.Bool
	lifecycle  context.Context
	revisions  *managementRevisionCodec
	peers      peerSnapshotter
}

type managementMIDI interface {
	Snapshot() midi.ManagerSnapshot
	Write(context.Context, music.NoteEvent) error
	Panic(context.Context) error
}

type peerSnapshotter interface {
	Snapshot() peer.Snapshot
}

type staticPeerSnapshot struct{ role string }

func (snapshot staticPeerSnapshot) Snapshot() peer.Snapshot {
	return peer.Snapshot{Role: snapshot.role, Nodes: []peer.NodeSnapshot{}}
}

// managementRevisionCodec keeps exact-byte repository digests behind an
// unguessable, process-local HTTP validator. The bounded lookup table permits
// a durable revision returned by a conflict to be translated back for an
// explicit out-of-sync reconciliation.
type managementRevisionCodec struct {
	key [sha256.Size]byte

	mu          sync.Mutex
	rawByPublic map[managementapi.Revision]config.Revision
	order       []managementapi.Revision
}

func newManagementBackend(
	controller *Controller,
	registry *flow.Registry,
	interfaces func() ([]capture.Interface, error),
	midiRuntime managementMIDI,
	ready *atomic.Bool,
	lifecycle context.Context,
) (*managementBackend, error) {
	if controller == nil {
		return nil, errors.New("management controller is required")
	}
	if registry == nil {
		return nil, errors.New("management flow registry is required")
	}
	if interfaces == nil {
		return nil, errors.New("management capture interface discovery is required")
	}
	if ready == nil {
		return nil, errors.New("management readiness is required")
	}
	revisions, err := newManagementRevisionCodec()
	if err != nil {
		return nil, fmt.Errorf("initialize management revision tokens: %w", err)
	}
	return &managementBackend{
		controller: controller,
		registry:   registry,
		interfaces: interfaces,
		midi:       midiRuntime,
		ready:      ready,
		lifecycle:  lifecycle,
		revisions:  revisions,
		peers:      staticPeerSnapshot{role: string(controller.Current().Config.Instance.Role)},
	}, nil
}

func newManagementRevisionCodec() (*managementRevisionCodec, error) {
	codec := &managementRevisionCodec{rawByPublic: make(map[managementapi.Revision]config.Revision)}
	if _, err := rand.Read(codec.key[:]); err != nil {
		return nil, err
	}
	return codec, nil
}

func newManagementRevisionCodecWithKey(key [sha256.Size]byte) *managementRevisionCodec {
	return &managementRevisionCodec{
		key:         key,
		rawByPublic: make(map[managementapi.Revision]config.Revision),
	}
}

func (codec *managementRevisionCodec) issue(raw config.Revision) managementapi.Revision {
	if raw == "" {
		return ""
	}
	public := codec.calculate(raw)
	codec.mu.Lock()
	defer codec.mu.Unlock()
	if _, exists := codec.rawByPublic[public]; exists {
		codec.rawByPublic[public] = raw
		return public
	}
	if len(codec.order) == maximumKnownManagementRevisions {
		delete(codec.rawByPublic, codec.order[0])
		copy(codec.order, codec.order[1:])
		codec.order = codec.order[:len(codec.order)-1]
	}
	codec.rawByPublic[public] = raw
	codec.order = append(codec.order, public)
	return public
}

func (codec *managementRevisionCodec) resolve(public managementapi.Revision, active config.Revision) config.Revision {
	if hmac.Equal([]byte(public), []byte(codec.calculate(active))) {
		return active
	}
	codec.mu.Lock()
	defer codec.mu.Unlock()
	return codec.rawByPublic[public]
}

func (codec *managementRevisionCodec) calculate(raw config.Revision) managementapi.Revision {
	mac := hmac.New(sha256.New, codec.key[:])
	_, _ = mac.Write([]byte("go-musical-packets/management-revision/v1\x00"))
	_, _ = mac.Write([]byte(raw))
	return managementapi.Revision(hex.EncodeToString(mac.Sum(nil)))
}

func (backend *managementBackend) Status(ctx context.Context) (managementapi.Status, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.Status{}, err
	}
	snapshot := backend.controller.store.current.Load()
	status := managementapi.Status{
		State:    string(snapshot.state),
		Revision: backend.revisions.issue(snapshot.revision),
		Writable: backend.controller.repository != nil,
	}
	switch snapshot.state {
	case ControllerStateRestartPending:
		status.PendingRevision = backend.revisions.issue(snapshot.pendingRevision)
		status.Warning = "configuration is saved and will take effect after restart"
	case ControllerStateDurabilityUncertain:
		status.Warning = "configuration durability is uncertain"
	case ControllerStateOutOfSync:
		status.Warning = "active and durable configuration are out of sync"
	case ControllerStateDegraded:
		status.Warning = "runtime configuration controller is degraded"
	}
	if !backend.ready.Load() {
		status.State = "unavailable"
		status.Warning = "runtime is starting or stopping"
	}
	return status, nil
}

func (backend *managementBackend) PendingConfig(ctx context.Context) (managementapi.ConfigDocument, error) {
	if ctx == nil {
		return managementapi.ConfigDocument{}, managementInvalid(errors.New("management pending configuration context is required"))
	}
	if err := ctx.Err(); err != nil {
		return managementapi.ConfigDocument{}, err
	}
	pending, ok := backend.controller.Pending()
	if !ok {
		return managementapi.ConfigDocument{}, &managementapi.BackendError{
			Kind:   managementapi.ErrorNotFound,
			Code:   "pending_config_not_found",
			Detail: "pending configuration was not found",
		}
	}
	return managementapi.ConfigDocument{
		Config:   pending.Config.Redacted(),
		Revision: backend.revisions.issue(pending.Revision),
	}, nil
}

func (backend *managementBackend) Config(ctx context.Context) (managementapi.ConfigDocument, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.ConfigDocument{}, err
	}
	document := backend.controller.Current()
	return managementapi.ConfigDocument{
		Config:   document.Config.Redacted(),
		Revision: backend.revisions.issue(document.Revision),
	}, nil
}

func (backend *managementBackend) ValidateConfig(ctx context.Context, candidate config.Config) (managementapi.Validation, error) {
	if ctx == nil {
		return managementapi.Validation{}, managementInvalid(errors.New("management validation context is required"))
	}
	backend.controller.mu.Lock()
	defer backend.controller.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return managementapi.Validation{}, err
	}
	current := backend.controller.store.current.Load()
	secretBase := current.configuration
	validationRevision := current.revision
	if current.state == ControllerStateRestartPending && current.pendingRevision != "" {
		secretBase = current.pendingConfiguration
		validationRevision = current.pendingRevision
	}
	resolved, err := config.ResolveRedacted(candidate, secretBase)
	if err != nil {
		return managementapi.Validation{}, managementInvalid(err)
	}
	if err := validateManagementSize(resolved); err != nil {
		return managementapi.Validation{}, managementInvalid(err)
	}
	validation, err := backend.controller.validate(current.configuration, resolved)
	if err != nil {
		return managementapi.Validation{}, managementInvalid(err)
	}
	return managementapi.Validation{
		Revision:              backend.revisions.issue(validationRevision),
		HotFields:             append(make([]string, 0, len(validation.HotFields)), validation.HotFields...),
		RestartRequiredFields: append(make([]string, 0, len(validation.RestartRequiredFields)), validation.RestartRequiredFields...),
	}, nil
}

func (backend *managementBackend) UpdateConfig(ctx context.Context, expected managementapi.Revision, candidate config.Config) (managementapi.ConfigDocument, error) {
	if ctx == nil {
		return managementapi.ConfigDocument{}, managementInvalid(errors.New("management update context is required"))
	}
	lifecycle := backend.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	if !backend.ready.Load() || lifecycle.Err() != nil {
		return managementapi.ConfigDocument{}, &managementapi.BackendError{
			Kind:   managementapi.ErrorUnavailable,
			Code:   "runtime_unavailable",
			Detail: "runtime is starting or stopping",
		}
	}
	current := backend.controller.Current()
	rawExpected := backend.revisions.resolve(expected, current.Revision)
	resolved, err := config.ResolveRedacted(candidate, current.Config)
	if err != nil {
		return managementapi.ConfigDocument{}, managementInvalid(err)
	}
	if err := validateManagementSize(resolved); err != nil {
		return managementapi.ConfigDocument{}, managementInvalid(err)
	}
	// Parenting the transaction directly to the runtime lifecycle makes
	// shutdown cancellation synchronous all the way to repository boundaries.
	// The request context is the second cancellation source.
	updateContext, cancelUpdate := context.WithCancel(lifecycle)
	stopRequestCancellation := context.AfterFunc(ctx, cancelUpdate)
	defer func() {
		stopRequestCancellation()
		cancelUpdate()
	}()
	// AfterFunc runs its callback asynchronously. Rechecking after registration
	// prevents a request cancellation that raced setup from entering the
	// persistence transaction before cancellation is visible.
	if ctx.Err() != nil {
		cancelUpdate()
	}
	if err := updateContext.Err(); err != nil {
		return managementapi.ConfigDocument{}, backend.managementUpdateError(err)
	}
	updated, err := backend.controller.UpdateContext(updateContext, rawExpected, resolved)
	if err != nil {
		return managementapi.ConfigDocument{}, backend.managementUpdateError(err)
	}
	return managementapi.ConfigDocument{
		Config:   updated.Config.Redacted(),
		Revision: backend.revisions.issue(updated.Revision),
	}, nil
}

func (backend *managementBackend) StageConfig(ctx context.Context, expected managementapi.Revision, candidate config.Config) (managementapi.ConfigDocument, error) {
	if ctx == nil {
		return managementapi.ConfigDocument{}, managementInvalid(errors.New("management pending configuration context is required"))
	}
	lifecycle := backend.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	if !backend.ready.Load() || lifecycle.Err() != nil {
		return managementapi.ConfigDocument{}, &managementapi.BackendError{
			Kind:   managementapi.ErrorUnavailable,
			Code:   "runtime_unavailable",
			Detail: "runtime is starting or stopping",
		}
	}
	current := backend.controller.Current()
	secretBase := current.Config
	if pending, ok := backend.controller.Pending(); ok {
		secretBase = pending.Config
	}
	rawExpected := backend.revisions.resolve(expected, current.Revision)
	resolved, err := config.ResolveRedacted(candidate, secretBase)
	if err != nil {
		return managementapi.ConfigDocument{}, managementInvalid(err)
	}
	if err := validateManagementSize(resolved); err != nil {
		return managementapi.ConfigDocument{}, managementInvalid(err)
	}
	updateContext, cancelUpdate := context.WithCancel(lifecycle)
	stopRequestCancellation := context.AfterFunc(ctx, cancelUpdate)
	defer func() {
		stopRequestCancellation()
		cancelUpdate()
	}()
	if ctx.Err() != nil {
		cancelUpdate()
	}
	if err := updateContext.Err(); err != nil {
		return managementapi.ConfigDocument{}, backend.managementUpdateError(err)
	}
	pending, err := backend.controller.StageRestartContext(updateContext, rawExpected, resolved)
	if err != nil {
		return managementapi.ConfigDocument{}, backend.managementUpdateError(err)
	}
	return managementapi.ConfigDocument{
		Config:   pending.Config.Redacted(),
		Revision: backend.revisions.issue(pending.Revision),
	}, nil
}

func (backend *managementBackend) CancelPendingConfig(ctx context.Context, expected managementapi.Revision) (managementapi.ConfigDocument, error) {
	if ctx == nil {
		return managementapi.ConfigDocument{}, managementInvalid(errors.New("management pending configuration context is required"))
	}
	lifecycle := backend.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	if !backend.ready.Load() || lifecycle.Err() != nil {
		return managementapi.ConfigDocument{}, &managementapi.BackendError{
			Kind:   managementapi.ErrorUnavailable,
			Code:   "runtime_unavailable",
			Detail: "runtime is starting or stopping",
		}
	}
	current := backend.controller.Current()
	rawExpected := backend.revisions.resolve(expected, current.Revision)
	updateContext, cancelUpdate := context.WithCancel(lifecycle)
	stopRequestCancellation := context.AfterFunc(ctx, cancelUpdate)
	defer func() {
		stopRequestCancellation()
		cancelUpdate()
	}()
	if ctx.Err() != nil {
		cancelUpdate()
	}
	updated, err := backend.controller.CancelRestartContext(updateContext, rawExpected)
	if err != nil {
		return managementapi.ConfigDocument{}, backend.managementUpdateError(err)
	}
	return managementapi.ConfigDocument{
		Config:   updated.Config.Redacted(),
		Revision: backend.revisions.issue(updated.Revision),
	}, nil
}

func validateManagementSize(candidate config.Config) error {
	contents, err := config.Encode(candidate)
	if err != nil {
		return err
	}
	redactedContents, err := config.Encode(candidate.Redacted())
	if err != nil {
		return err
	}
	// Admission is based on a fixed public-representation allowance plus a
	// fixed secret allowance. Basing the boundary directly on the resolved
	// byte count would let callers infer hidden secret lengths by padding an
	// otherwise valid candidate around MaximumBytes.
	if len(redactedContents) > config.MaximumBytes-managementSecretExpansionBudget ||
		len(contents)-len(redactedContents) > managementSecretExpansionBudget {
		return errors.New("canonical configuration exceeds the maximum size")
	}
	return nil
}

func managementInvalid(err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorInvalid,
		Code:   "invalid_config",
		Detail: err.Error(),
		Err:    err,
	}
}

func (backend *managementBackend) managementUpdateError(err error) error {
	var conflict *config.ConflictError
	if errors.As(err, &conflict) {
		return &managementapi.BackendError{
			Kind:           managementapi.ErrorPreconditionFailed,
			Code:           "revision_conflict",
			Detail:         "configuration revision does not match the current durable revision",
			ActualRevision: backend.revisions.issue(conflict.Actual),
			Err:            err,
		}
	}
	var restart *RestartRequiredError
	if errors.As(err, &restart) {
		return &managementapi.BackendError{
			Kind:   managementapi.ErrorConflict,
			Code:   "restart_required",
			Detail: "configuration changes require a process restart",
			Fields: append([]string(nil), restart.Fields...),
			Err:    err,
		}
	}
	var pending *RestartPendingError
	if errors.As(err, &pending) {
		return &managementapi.BackendError{
			Kind:   managementapi.ErrorConflict,
			Code:   "restart_pending",
			Detail: "restart the process or discard the pending configuration before making persisted changes",
			Err:    err,
		}
	}
	var notRequired *RestartNotRequiredError
	if errors.As(err, &notRequired) {
		return &managementapi.BackendError{
			Kind:   managementapi.ErrorConflict,
			Code:   "restart_not_required",
			Detail: "configuration can be applied safely without restart",
			Err:    err,
		}
	}
	var notFound *PendingConfigNotFoundError
	if errors.As(err, &notFound) {
		return &managementapi.BackendError{
			Kind:   managementapi.ErrorNotFound,
			Code:   "pending_config_not_found",
			Detail: "pending configuration was not found",
			Err:    err,
		}
	}
	var readOnly *ReadOnlyError
	if errors.As(err, &readOnly) {
		return &managementapi.BackendError{
			Kind:   managementapi.ErrorConflict,
			Code:   "read_only",
			Detail: "runtime configuration is read-only",
			Err:    err,
		}
	}
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorUnavailable,
		Code:   "update_unavailable",
		Detail: "configuration update is temporarily unavailable",
		Err:    err,
	}
}
