package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

const maximumOverlayFlows = 10_000

// ConfigRepository is the durable compare-and-swap boundary used by the
// runtime configuration controller.
type ConfigRepository interface {
	Read() (config.Snapshot, error)
	Replace(config.Revision, config.Config) (config.Change, error)
	Rollback(config.Change) (config.Change, error)
}

// ControllerState describes whether the in-memory policy and durable file are
// known to agree.
type ControllerState string

const (
	ControllerStateReady               ControllerState = "ready"
	ControllerStateRestartPending      ControllerState = "restart_pending"
	ControllerStateOutOfSync           ControllerState = "out_of_sync"
	ControllerStateDurabilityUncertain ControllerState = "durability_uncertain"
	ControllerStateDegraded            ControllerState = "degraded"
)

// Document is a detached view of the active configuration generation.
type Document struct {
	Config   config.Config   `json:"config"`
	Revision config.Revision `json:"revision"`
	Writable bool            `json:"writable"`
	State    ControllerState `json:"state"`
	Warning  string          `json:"warning,omitempty"`
}

// Validation classifies fields changed by a proposed configuration. Hot
// fields may be published without rebuilding native runtime components.
type Validation struct {
	HotFields             []string `json:"hot_fields"`
	RestartRequiredFields []string `json:"restart_required_fields"`
}

// RestartRequiredError rejects valid changes that cannot safely be applied to
// the running process. Fields are stable configuration paths in schema order.
type RestartRequiredError struct {
	Fields []string
}

// RestartPendingError rejects active-policy writes while a different durable
// generation is waiting for the next process start.
type RestartPendingError struct{}

func (*RestartPendingError) Error() string { return "configuration restart is pending" }

// RestartNotRequiredError rejects staging when the candidate can be applied
// safely to the active process instead.
type RestartNotRequiredError struct{}

func (*RestartNotRequiredError) Error() string { return "configuration does not require restart" }

// PendingConfigNotFoundError reports that no next-start generation exists.
type PendingConfigNotFoundError struct{}

func (*PendingConfigNotFoundError) Error() string { return "pending configuration was not found" }

func (err *RestartRequiredError) Error() string {
	return "configuration changes require restart: " + strings.Join(err.Fields, ", ")
}

// ReadOnlyError reports that this runtime was not started with a config path.
type ReadOnlyError struct{}

func (*ReadOnlyError) Error() string { return "runtime configuration is read-only" }

// policyStateError rejects temporary policy mutations while the controller is
// not known to agree with durable configuration.
type policyStateError struct {
	state ControllerState
}

func (err *policyStateError) Error() string {
	return fmt.Sprintf("runtime configuration state is %s", err.state)
}

// DegradedError reports that a failed publication could not be rolled back
// durably, so further persisted changes are unsafe.
type DegradedError struct {
	Reason string
}

func (err *DegradedError) Error() string {
	if err.Reason == "" {
		return "runtime configuration controller is degraded"
	}
	return "runtime configuration controller is degraded: " + err.Reason
}

type policySnapshot struct {
	revision             config.Revision
	configuration        config.Config
	pendingRevision      config.Revision
	pendingConfiguration config.Config
	selector             *flow.Selector
	overlay              flow.Overlay
	state                ControllerState
	warning              string
}

// policyStore is both the publication boundary and the pipeline selector. Its
// Evaluate method intentionally ignores the pipeline's independently supplied
// overlay: selector and overlay must come from the same atomic generation.
type policyStore struct {
	current atomic.Pointer[policySnapshot]
}

func newPolicyStore(snapshot *policySnapshot) *policyStore {
	store := &policyStore{}
	store.current.Store(snapshot)
	return store
}

func (store *policyStore) Evaluate(event packet.Event, _ flow.Overlay) (flow.Selection, error) {
	snapshot := store.current.Load()
	if snapshot == nil || snapshot.selector == nil {
		return flow.Selection{}, errors.New("runtime policy is unavailable")
	}
	return snapshot.selector.Evaluate(event, snapshot.overlay)
}

func (store *policyStore) publish(snapshot *policySnapshot) { store.current.Store(snapshot) }

// Controller validates, persists, and atomically publishes runtime policy.
// Configuration changes and overlay changes are serialized so neither can
// accidentally publish a generation built from stale state.
type Controller struct {
	mu         sync.Mutex
	store      *policyStore
	repository ConfigRepository
	safety     func([]flow.Rule) []flow.Rule
	apply      func(*policySnapshot) error
}

// newController loads the repository when present, making that snapshot
// authoritative over the separately supplied configuration.
func newController(configuration config.Config, repository ConfigRepository, safety func([]flow.Rule) []flow.Rule) (*Controller, error) {
	var revision config.Revision
	if repository != nil {
		snapshot, err := repository.Read()
		if err != nil {
			return nil, fmt.Errorf("read runtime configuration: %w", err)
		}
		configuration = snapshot.Config
		revision = snapshot.Revision
	} else {
		var err error
		revision, err = canonicalRevision(configuration)
		if err != nil {
			return nil, err
		}
	}
	if err := configuration.Validate(); err != nil {
		return nil, fmt.Errorf("validate authoritative runtime configuration: %w", err)
	}

	selector, err := compileSelector(configuration, safety)
	if err != nil {
		return nil, err
	}
	initial := &policySnapshot{
		revision:      revision,
		configuration: configuration.Clone(),
		selector:      selector,
		overlay:       flow.Overlay{},
		state:         ControllerStateReady,
	}
	controller := &Controller{
		store:      newPolicyStore(initial),
		repository: repository,
		safety:     safety,
	}
	controller.apply = func(next *policySnapshot) error {
		controller.store.publish(next)
		return nil
	}
	return controller, nil
}

func canonicalRevision(configuration config.Config) (config.Revision, error) {
	contents, err := config.Encode(configuration)
	if err != nil {
		return "", fmt.Errorf("encode in-memory configuration: %w", err)
	}
	sum := sha256.Sum256(contents)
	return config.Revision(hex.EncodeToString(sum[:])), nil
}

// Current returns a deep copy of the active document.
func (controller *Controller) Current() Document {
	snapshot := controller.store.current.Load()
	return Document{
		Config:   snapshot.configuration.Clone(),
		Revision: snapshot.revision,
		Writable: controller.repository != nil,
		State:    snapshot.state,
		Warning:  snapshot.warning,
	}
}

// Pending returns the validated durable generation waiting for the next
// process start. The returned value is detached from controller storage.
func (controller *Controller) Pending() (config.Snapshot, bool) {
	snapshot := controller.store.current.Load()
	if snapshot == nil || snapshot.state != ControllerStateRestartPending || snapshot.pendingRevision == "" {
		return config.Snapshot{}, false
	}
	return config.Snapshot{
		Config:   snapshot.pendingConfiguration.Clone(),
		Revision: snapshot.pendingRevision,
	}, true
}

// Overlay returns detached mute and solo maps for the active generation.
func (controller *Controller) Overlay() flow.Overlay {
	return cloneOverlay(controller.store.current.Load().overlay)
}

// Validate validates and classifies candidate without persistence or
// publication.
func (controller *Controller) Validate(candidate config.Config) (Validation, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	current := controller.store.current.Load()
	return controller.validate(current.configuration, candidate.Clone())
}

func (controller *Controller) validate(current, candidate config.Config) (Validation, error) {
	if err := candidate.Validate(); err != nil {
		return Validation{}, fmt.Errorf("validate runtime configuration: %w", err)
	}
	classification := classifyChanges(current, candidate)
	if _, err := compileSelector(candidate, controller.safety); err != nil {
		return Validation{}, err
	}
	return classification, nil
}

// Update performs a serialized validate, classify, compile, persist, publish
// transaction. Persistence precedes the infallible production atomic publish.
func (controller *Controller) Update(expected config.Revision, candidate config.Config) (Document, error) {
	return controller.UpdateContext(context.Background(), expected, candidate)
}

// ConfigMutation changes a controller-owned configuration clone. Mutations
// must be synchronous, side-effect free, and must not re-enter Controller.
type ConfigMutation func(*config.Config) error

// Mutate performs an atomic read-modify-write transaction. During explicit
// out-of-sync reconciliation, mutation is based on the durable configuration
// so unrelated external edits are not overwritten.
func (controller *Controller) Mutate(expected config.Revision, mutation ConfigMutation) (Document, error) {
	return controller.MutateContext(context.Background(), expected, mutation)
}

// MutateContext is Mutate with cancellation propagated through persistence.
func (controller *Controller) MutateContext(ctx context.Context, expected config.Revision, mutation ConfigMutation) (Document, error) {
	if mutation == nil {
		return controller.Current(), errors.New("runtime policy mutation is required")
	}
	return controller.transactContext(ctx, expected, func(base config.Config) (config.Config, error) {
		candidate := base.Clone()
		if err := mutation(&candidate); err != nil {
			return config.Config{}, err
		}
		return candidate.Clone(), nil
	})
}

// UpdateContext is Update with cancellation propagated to repositories that
// support cancelable mutation. A successful durable commit is still published
// even if the request is canceled after the commit boundary.
func (controller *Controller) UpdateContext(ctx context.Context, expected config.Revision, candidate config.Config) (Document, error) {
	return controller.transactContext(ctx, expected, func(config.Config) (config.Config, error) {
		return candidate.Clone(), nil
	})
}

// StageRestartContext validates and durably stores a complete next-start
// generation without publishing it to active runtime components.
func (controller *Controller) StageRestartContext(ctx context.Context, expected config.Revision, candidate config.Config) (config.Snapshot, error) {
	if ctx == nil {
		return config.Snapshot{}, errors.New("pending configuration context is required")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return config.Snapshot{}, err
	}
	current := controller.store.current.Load()
	if current.state == ControllerStateDegraded {
		return config.Snapshot{}, &DegradedError{Reason: current.warning}
	}
	if current.state != ControllerStateReady && current.state != ControllerStateRestartPending {
		return config.Snapshot{}, &policyStateError{state: current.state}
	}
	durableRevision := current.revision
	if current.state == ControllerStateRestartPending {
		durableRevision = current.pendingRevision
	}
	if expected != durableRevision {
		return config.Snapshot{}, &config.ConflictError{Expected: expected, Actual: durableRevision}
	}
	candidate = candidate.Clone()
	classification, err := controller.validate(current.configuration, candidate)
	if err != nil {
		return config.Snapshot{}, err
	}
	if len(classification.RestartRequiredFields) == 0 {
		return config.Snapshot{}, &RestartNotRequiredError{}
	}
	if controller.repository == nil {
		return config.Snapshot{}, &ReadOnlyError{}
	}
	if err := ctx.Err(); err != nil {
		return config.Snapshot{}, err
	}
	change, err := controller.replace(ctx, durableRevision, candidate)
	if err != nil {
		var conflict *config.ConflictError
		if errors.As(err, &conflict) && conflict.Actual != durableRevision {
			controller.store.publish(snapshotWithStatus(
				current,
				ControllerStateOutOfSync,
				fmt.Sprintf("configuration file revision is %s while expected pending revision is %s", conflict.Actual, durableRevision),
			))
		} else if !errors.As(err, &conflict) {
			controller.markPersistenceUncertainty(current, err)
		}
		return config.Snapshot{}, fmt.Errorf("persist pending configuration: %w", err)
	}
	next := snapshotWithStatus(current, ControllerStateRestartPending, "configuration saved for next restart")
	next.pendingRevision = change.After.Revision
	next.pendingConfiguration = change.After.Config.Clone()
	if change.DurabilityWarning != nil {
		next.warning = "configuration saved for next restart; durability is uncertain: " + change.DurabilityWarning.Error()
	}
	controller.store.publish(next)
	return config.Snapshot{Config: change.After.Config.Clone(), Revision: change.After.Revision}, nil
}

// CancelRestartContext restores the active generation as the durable startup
// configuration and clears the pending marker.
func (controller *Controller) CancelRestartContext(ctx context.Context, expected config.Revision) (Document, error) {
	if ctx == nil {
		return controller.Current(), errors.New("pending configuration context is required")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return controller.Current(), err
	}
	current := controller.store.current.Load()
	if current.state != ControllerStateRestartPending || current.pendingRevision == "" {
		return controller.Current(), &PendingConfigNotFoundError{}
	}
	if expected != current.pendingRevision {
		return controller.Current(), &config.ConflictError{Expected: expected, Actual: current.pendingRevision}
	}
	change, err := controller.replace(ctx, current.pendingRevision, current.configuration.Clone())
	if err != nil {
		var conflict *config.ConflictError
		if errors.As(err, &conflict) && conflict.Actual != current.pendingRevision {
			controller.store.publish(snapshotWithStatus(
				current,
				ControllerStateOutOfSync,
				fmt.Sprintf("configuration file revision is %s while pending revision is %s", conflict.Actual, current.pendingRevision),
			))
		} else if !errors.As(err, &conflict) {
			controller.markPersistenceUncertainty(current, err)
		}
		return controller.Current(), fmt.Errorf("discard pending configuration: %w", err)
	}
	next := snapshotWithStatus(current, ControllerStateReady, "")
	next.revision = change.After.Revision
	next.pendingRevision = ""
	next.pendingConfiguration = config.Config{}
	if change.DurabilityWarning != nil {
		next.state = ControllerStateDurabilityUncertain
		next.warning = change.DurabilityWarning.Error()
	}
	controller.store.publish(next)
	return controller.Current(), nil
}

type configCandidateBuilder func(config.Config) (config.Config, error)

func (controller *Controller) transactContext(ctx context.Context, expected config.Revision, build configCandidateBuilder) (Document, error) {
	if ctx == nil {
		return controller.Current(), errors.New("runtime policy update context is required")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return controller.Current(), err
	}

	current := controller.store.current.Load()
	if current.state == ControllerStateDegraded {
		return controller.Current(), &DegradedError{Reason: current.warning}
	}
	if current.state == ControllerStateRestartPending {
		return controller.Current(), &RestartPendingError{}
	}
	if current.state != ControllerStateOutOfSync && expected != current.revision {
		return controller.Current(), &config.ConflictError{Expected: expected, Actual: current.revision}
	}
	base := current.configuration.Clone()
	// An out-of-sync retry is an explicit reconciliation. Re-read the durable
	// document and require the caller to identify that exact revision before
	// allowing the normal hot-field and repository CAS checks to proceed. A
	// read-modify-write mutation is based on this durable document; a full
	// replacement builder deliberately ignores it.
	if current.state == ControllerStateOutOfSync {
		durable, readErr := controller.repository.Read()
		if readErr != nil {
			return controller.Current(), fmt.Errorf("read configuration for reconciliation: %w", readErr)
		}
		if durable.Revision != expected {
			conflict := &config.ConflictError{Expected: expected, Actual: durable.Revision}
			controller.store.publish(snapshotWithStatus(
				current,
				ControllerStateOutOfSync,
				fmt.Sprintf("configuration file revision is %s while active revision is %s", durable.Revision, current.revision),
			))
			return controller.Current(), conflict
		}
		base = durable.Config.Clone()
	}
	candidate, err := build(base)
	if err != nil {
		return controller.Current(), err
	}
	if err := ctx.Err(); err != nil {
		return controller.Current(), err
	}
	candidate = candidate.Clone()
	classification, err := controller.validate(current.configuration, candidate)
	if err != nil {
		return controller.Current(), err
	}
	if len(classification.RestartRequiredFields) != 0 {
		return controller.Current(), &RestartRequiredError{Fields: append([]string(nil), classification.RestartRequiredFields...)}
	}
	selector, err := compileSelector(candidate, controller.safety)
	if err != nil {
		return controller.Current(), err
	}
	if controller.repository == nil {
		if expected != current.revision {
			return controller.Current(), &config.ConflictError{Expected: expected, Actual: current.revision}
		}
		return controller.Current(), &ReadOnlyError{}
	}
	if err := ctx.Err(); err != nil {
		return controller.Current(), err
	}

	change, err := controller.replace(ctx, expected, candidate.Clone())
	if err != nil {
		var conflict *config.ConflictError
		if errors.As(err, &conflict) && conflict.Actual != current.revision {
			controller.store.publish(snapshotWithStatus(
				current,
				ControllerStateOutOfSync,
				fmt.Sprintf("configuration file revision is %s while active revision is %s", conflict.Actual, current.revision),
			))
		} else if !errors.As(err, &conflict) {
			controller.markPersistenceUncertainty(current, err)
		}
		return controller.Current(), fmt.Errorf("persist runtime configuration: %w", err)
	}
	// A repository no-op performs no new durability operation. Preserve an
	// existing warning (and its state) when disk and active policy already
	// match. Out-of-sync reconciliation is the exception: its explicit Read
	// above verified the durable revision and may restore Ready.
	if !change.Changed() &&
		change.After.Revision == current.revision &&
		reflect.DeepEqual(change.After.Config, current.configuration) &&
		current.state != ControllerStateOutOfSync {
		return controller.Current(), nil
	}
	next := &policySnapshot{
		revision:      change.After.Revision,
		configuration: change.After.Config.Clone(),
		selector:      selector,
		overlay:       cloneOverlay(current.overlay),
		state:         ControllerStateReady,
	}
	if change.DurabilityWarning != nil {
		next.warning = change.DurabilityWarning.Error()
		next.state = ControllerStateDurabilityUncertain
	}
	if err := controller.apply(next); err != nil {
		return controller.applyFailed(current, change, err)
	}
	return controller.Current(), nil
}

type contextualConfigRepository interface {
	ReplaceContext(context.Context, config.Revision, config.Config) (config.Change, error)
	RollbackContext(context.Context, config.Change) (config.Change, error)
}

func (controller *Controller) replace(ctx context.Context, expected config.Revision, candidate config.Config) (config.Change, error) {
	if repository, ok := controller.repository.(contextualConfigRepository); ok {
		return repository.ReplaceContext(ctx, expected, candidate)
	}
	return controller.repository.Replace(expected, candidate)
}

func (controller *Controller) markPersistenceUncertainty(current *policySnapshot, persistErr error) {
	durable, err := controller.repository.Read()
	expectedDurable := current.revision
	if current.state == ControllerStateRestartPending && current.pendingRevision != "" {
		expectedDurable = current.pendingRevision
	}
	if err == nil && durable.Revision == expectedDurable {
		return
	}
	warning := fmt.Sprintf("configuration persistence failed and durable state could not be verified: %v", persistErr)
	if err != nil {
		warning += fmt.Sprintf("; read failed: %v", err)
	} else {
		warning += fmt.Sprintf("; file revision is %s while expected durable revision is %s", durable.Revision, expectedDurable)
	}
	controller.store.publish(snapshotWithStatus(current, ControllerStateOutOfSync, warning))
}

func (controller *Controller) applyFailed(current *policySnapshot, change config.Change, applyErr error) (Document, error) {
	if !change.Changed() {
		if change.After.Revision != current.revision || !reflect.DeepEqual(change.After.Config, current.configuration) {
			controller.store.publish(snapshotWithStatus(current, ControllerStateOutOfSync, "persisted configuration differs from the active policy after publication failure"))
		}
		return controller.Current(), fmt.Errorf("publish runtime configuration: %w", applyErr)
	}
	rollback, rollbackErr := controller.rollback(change)
	if rollbackErr != nil {
		warning := errors.Join(applyErr, rollbackErr).Error()
		controller.store.publish(snapshotWithStatus(current, ControllerStateDegraded, warning))
		return controller.Current(), &policyApplyError{apply: applyErr, rollback: rollbackErr}
	}
	if rollback.DurabilityWarning != nil {
		controller.store.publish(snapshotWithStatus(current, ControllerStateDurabilityUncertain, rollback.DurabilityWarning.Error()))
		return controller.Current(), &policyApplyError{apply: applyErr, rollbackWarning: rollback.DurabilityWarning}
	}
	controller.store.publish(snapshotWithStatus(current, ControllerStateReady, ""))
	return controller.Current(), &policyApplyError{apply: applyErr}
}

func (controller *Controller) rollback(change config.Change) (config.Change, error) {
	if repository, ok := controller.repository.(contextualConfigRepository); ok {
		// Once persistence succeeds, rollback must complete independently of the
		// client connection so active and durable policy do not diverge.
		return repository.RollbackContext(context.Background(), change)
	}
	return controller.repository.Rollback(change)
}

func snapshotWithStatus(current *policySnapshot, state ControllerState, warning string) *policySnapshot {
	return &policySnapshot{
		revision:             current.revision,
		configuration:        current.configuration.Clone(),
		pendingRevision:      current.pendingRevision,
		pendingConfiguration: current.pendingConfiguration.Clone(),
		selector:             current.selector,
		overlay:              cloneOverlay(current.overlay),
		state:                state,
		warning:              warning,
	}
}

type policyApplyError struct {
	apply           error
	rollback        error
	rollbackWarning error
}

func (err *policyApplyError) Error() string {
	switch {
	case err.rollback != nil:
		return fmt.Sprintf("publish runtime configuration: %v; rollback failed: %v", err.apply, err.rollback)
	case err.rollbackWarning != nil:
		return fmt.Sprintf("publish runtime configuration: %v; rollback durability warning: %v", err.apply, err.rollbackWarning)
	default:
		return fmt.Sprintf("publish runtime configuration: %v; persisted change rolled back", err.apply)
	}
}

func (err *policyApplyError) Unwrap() []error {
	result := make([]error, 0, 3)
	for _, candidate := range []error{err.apply, err.rollback, err.rollbackWarning} {
		if candidate != nil {
			result = append(result, candidate)
		}
	}
	return result
}

// ReplaceMute atomically replaces the temporary mute set while preserving the
// active revision, configuration, selector, and solo set.
func (controller *Controller) ReplaceMute(muted map[string]struct{}) (flow.Overlay, error) {
	return controller.ReplaceMuteContext(context.Background(), muted)
}

// ReplaceMuteContext is ReplaceMute with cancellation checked under the
// controller's publication lock.
func (controller *Controller) ReplaceMuteContext(ctx context.Context, muted map[string]struct{}) (flow.Overlay, error) {
	return controller.replaceOverlay(ctx, muted, nil, true)
}

// ReplaceSolo atomically replaces the temporary solo set while preserving the
// active revision, configuration, selector, and mute set.
func (controller *Controller) ReplaceSolo(soloed map[string]struct{}) (flow.Overlay, error) {
	return controller.ReplaceSoloContext(context.Background(), soloed)
}

// ReplaceSoloContext is ReplaceSolo with cancellation checked under the
// controller's publication lock.
func (controller *Controller) ReplaceSoloContext(ctx context.Context, soloed map[string]struct{}) (flow.Overlay, error) {
	return controller.replaceOverlay(ctx, nil, soloed, false)
}

func (controller *Controller) replaceOverlay(ctx context.Context, muted, soloed map[string]struct{}, replaceMute bool) (flow.Overlay, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	current := controller.store.current.Load()
	if ctx == nil {
		return cloneOverlay(current.overlay), errors.New("runtime overlay context is required")
	}
	if err := ctx.Err(); err != nil {
		return cloneOverlay(current.overlay), err
	}
	if current.state != ControllerStateReady && current.state != ControllerStateRestartPending {
		return cloneOverlay(current.overlay), &policyStateError{state: current.state}
	}
	limit := current.configuration.Performance.FlowRegistryCapacity
	if limit > maximumOverlayFlows {
		limit = maximumOverlayFlows
	}
	values := soloed
	if replaceMute {
		values = muted
	}
	if err := validateOverlaySet(values, limit); err != nil {
		return cloneOverlay(current.overlay), err
	}
	next := snapshotWithStatus(current, current.state, current.warning)
	if replaceMute {
		next.overlay.Muted = cloneSet(muted)
	} else {
		next.overlay.Soloed = cloneSet(soloed)
	}
	if err := ctx.Err(); err != nil {
		return cloneOverlay(current.overlay), err
	}
	controller.store.publish(next)
	return cloneOverlay(next.overlay), nil
}

// configureSafety compiles and publishes the current configuration with a new
// safety-rule callback. It is intended for the one-time startup transition
// after an ephemeral listener port becomes known.
func (controller *Controller) configureSafety(safety func([]flow.Rule) []flow.Rule) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	current := controller.store.current.Load()
	selector, err := compileSelector(current.configuration, safety)
	if err != nil {
		return err
	}
	next := &policySnapshot{
		revision:      current.revision,
		configuration: current.configuration.Clone(),
		selector:      selector,
		overlay:       cloneOverlay(current.overlay),
		state:         current.state,
		warning:       current.warning,
	}
	controller.safety = safety
	controller.store.publish(next)
	return nil
}

func validateOverlaySet(values map[string]struct{}, limit int) error {
	if len(values) > limit {
		return fmt.Errorf("overlay contains %d flow IDs; maximum is %d", len(values), limit)
	}
	for value := range values {
		if len(value) != 24 {
			return fmt.Errorf("overlay flow ID %q must be 24 lowercase hexadecimal characters", value)
		}
		decoded, err := hex.DecodeString(value)
		if err != nil || hex.EncodeToString(decoded) != value {
			return fmt.Errorf("overlay flow ID %q must be 24 lowercase hexadecimal characters", value)
		}
	}
	return nil
}

func cloneOverlay(overlay flow.Overlay) flow.Overlay {
	return flow.Overlay{Muted: cloneSet(overlay.Muted), Soloed: cloneSet(overlay.Soloed)}
}

func cloneSet(values map[string]struct{}) map[string]struct{} {
	if values == nil {
		return nil
	}
	clone := make(map[string]struct{}, len(values))
	for value := range values {
		clone[value] = struct{}{}
	}
	return clone
}

func compileSelector(configuration config.Config, safety func([]flow.Rule) []flow.Rule) (*flow.Selector, error) {
	persistent, err := configuration.FlowRules()
	if err != nil {
		return nil, fmt.Errorf("build flow rules: %w", err)
	}
	pinned := make([]flow.Rule, 0, len(persistent))
	user := make([]flow.Rule, 0, len(persistent))
	for _, rule := range persistent {
		if rule.Match.ExactFlowID != "" {
			pinned = append(pinned, rule)
		} else {
			user = append(user, rule)
		}
	}
	var safetyRules []flow.Rule
	if safety != nil {
		safetyRules = safety(persistent)
	}
	selector, err := flow.NewSelector(flow.SelectorConfig{
		Seed:        configuration.Mapping.Seed,
		Default:     flow.Action{State: flow.State(configuration.Mapping.DefaultState), Channel: configuration.Mapping.DefaultChannel},
		SafetyRules: safetyRules,
		PinnedRules: pinned,
		UserRules:   user,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize flow selector: %w", err)
	}
	return selector, nil
}

func classifyChanges(current, candidate config.Config) Validation {
	var hot, restart []string
	appendIfChanged := func(target *[]string, path string, before, after any) {
		if !reflect.DeepEqual(before, after) {
			*target = append(*target, path)
		}
	}

	appendIfChanged(&restart, "instance.id", current.Instance.ID, candidate.Instance.ID)
	appendIfChanged(&restart, "instance.role", current.Instance.Role, candidate.Instance.Role)
	appendIfChanged(&restart, "capture.enabled", current.Capture.Enabled, candidate.Capture.Enabled)
	appendIfChanged(&restart, "capture.interface", current.Capture.Interface, candidate.Capture.Interface)
	appendIfChanged(&restart, "capture.bpf", current.Capture.BPF, candidate.Capture.BPF)
	appendIfChanged(&restart, "capture.snapshot_length", current.Capture.SnapshotLength, candidate.Capture.SnapshotLength)
	appendIfChanged(&restart, "capture.promiscuous", current.Capture.Promiscuous, candidate.Capture.Promiscuous)
	appendIfChanged(&restart, "mapping.version", current.Mapping.Version, candidate.Mapping.Version)
	appendIfChanged(&restart, "mapping.seed", current.Mapping.Seed, candidate.Mapping.Seed)
	appendIfChanged(&hot, "mapping.default_state", current.Mapping.DefaultState, candidate.Mapping.DefaultState)
	appendIfChanged(&hot, "mapping.default_channel", current.Mapping.DefaultChannel, candidate.Mapping.DefaultChannel)
	appendIfChanged(&restart, "mapping.minimum_note", current.Mapping.MinimumNote, candidate.Mapping.MinimumNote)
	appendIfChanged(&restart, "mapping.maximum_note", current.Mapping.MaximumNote, candidate.Mapping.MaximumNote)
	appendIfChanged(&restart, "mapping.minimum_duration", current.Mapping.MinimumDuration, candidate.Mapping.MinimumDuration)
	appendIfChanged(&restart, "mapping.maximum_duration", current.Mapping.MaximumDuration, candidate.Mapping.MaximumDuration)
	appendIfChanged(&restart, "performance.packet_queue_capacity", current.Performance.PacketQueueCapacity, candidate.Performance.PacketQueueCapacity)
	appendIfChanged(&restart, "performance.note_queue_capacity", current.Performance.NoteQueueCapacity, candidate.Performance.NoteQueueCapacity)
	appendIfChanged(&restart, "performance.ui_queue_capacity", current.Performance.UIQueueCapacity, candidate.Performance.UIQueueCapacity)
	appendIfChanged(&restart, "performance.flow_registry_capacity", current.Performance.FlowRegistryCapacity, candidate.Performance.FlowRegistryCapacity)
	appendIfChanged(&restart, "performance.flow_ttl", current.Performance.FlowTTL, candidate.Performance.FlowTTL)
	appendIfChanged(&restart, "performance.maximum_notes_per_second", current.Performance.MaximumNotesPerSecond, candidate.Performance.MaximumNotesPerSecond)
	appendIfChanged(&restart, "performance.maximum_polyphony", current.Performance.MaximumPolyphony, candidate.Performance.MaximumPolyphony)
	appendIfChanged(&restart, "performance.minimum_retrigger_interval", current.Performance.MinimumRetriggerInterval, candidate.Performance.MinimumRetriggerInterval)
	appendIfChanged(&restart, "midi.enabled", current.MIDI.Enabled, candidate.MIDI.Enabled)
	appendIfChanged(&restart, "midi.exact_device_name", current.MIDI.ExactDeviceName, candidate.MIDI.ExactDeviceName)
	appendIfChanged(&restart, "midi.device_name_regexp", current.MIDI.DeviceNameRegexp, candidate.MIDI.DeviceNameRegexp)
	appendIfChanged(&restart, "midi.poll_interval", current.MIDI.PollInterval, candidate.MIDI.PollInterval)
	appendIfChanged(&restart, "server.listen_address", current.Server.ListenAddress, candidate.Server.ListenAddress)
	appendIfChanged(&restart, "server.read_timeout", current.Server.ReadTimeout, candidate.Server.ReadTimeout)
	appendIfChanged(&restart, "server.write_timeout", current.Server.WriteTimeout, candidate.Server.WriteTimeout)
	appendIfChanged(&restart, "peer.enabled", current.Peer.Enabled, candidate.Peer.Enabled)
	appendIfChanged(&restart, "peer.url", current.Peer.URL, candidate.Peer.URL)
	appendIfChanged(&restart, "peer.token", current.Peer.Token, candidate.Peer.Token)
	appendIfChanged(&restart, "peer.queue_capacity", current.Peer.QueueCapacity, candidate.Peer.QueueCapacity)
	appendIfChanged(&restart, "peer.maximum_connections", current.Peer.MaximumConnections, candidate.Peer.MaximumConnections)
	appendIfChanged(&restart, "peer.recent_ttl", current.Peer.RecentTTL, candidate.Peer.RecentTTL)
	appendIfChanged(&restart, "peer.reconnect_base", current.Peer.ReconnectBase, candidate.Peer.ReconnectBase)
	appendIfChanged(&restart, "peer.reconnect_limit", current.Peer.ReconnectLimit, candidate.Peer.ReconnectLimit)
	appendIfChanged(&restart, "peer.stale_after", current.Peer.StaleAfter, candidate.Peer.StaleAfter)
	appendIfChanged(&restart, "metrics.namespace", current.Metrics.Namespace, candidate.Metrics.Namespace)
	appendIfChanged(&restart, "logging.level", current.Logging.Level, candidate.Logging.Level)
	appendIfChanged(&restart, "logging.format", current.Logging.Format, candidate.Logging.Format)
	appendIfChanged(&hot, "rules", current.Rules, candidate.Rules)

	// Rules and mapping defaults are the complete hot-field allowlist. If the
	// schema gains a field before this classifier is updated, reject that change
	// instead of silently treating it as hot.
	currentWithoutHot := current.Clone()
	candidateWithoutHot := candidate.Clone()
	candidateWithoutHot.Mapping.DefaultState = currentWithoutHot.Mapping.DefaultState
	candidateWithoutHot.Mapping.DefaultChannel = currentWithoutHot.Mapping.DefaultChannel
	candidateWithoutHot.Rules = currentWithoutHot.Rules
	if len(restart) == 0 && !reflect.DeepEqual(currentWithoutHot, candidateWithoutHot) {
		restart = append(restart, "unclassified")
	}

	// Defensive sorting makes the contract stable if fields are reorganized.
	sort.Strings(hot)
	sort.Strings(restart)
	return Validation{HotFields: hot, RestartRequiredFields: restart}
}

// Ensure policyStore continues to satisfy the packet-pipeline boundary.
var _ interface {
	Evaluate(packet.Event, flow.Overlay) (flow.Selection, error)
} = (*policyStore)(nil)
