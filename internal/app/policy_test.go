package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestControllerUsesPinnedOverlayAndSafetyPrecedence(t *testing.T) {
	configuration := testConfig()
	event := testPacket(41000, 443, time.Unix(100, 0))
	key, _ := flow.Canonicalize(event)
	flowID := key.ID(configuration.Mapping.Seed)
	configuration.Rules = config.RulesConfig{
		{ID: "broad-first", Enabled: true, Match: config.RuleMatchConfig{Protocol: packet.ProtocolTCP}, Action: config.RuleActionConfig{State: config.FlowPlay, Channel: 2}},
		{ID: "exact-later", Enabled: true, Match: config.RuleMatchConfig{ExactFlowID: flowID}, Action: config.RuleActionConfig{State: config.FlowPlay, Channel: 7}},
	}

	controller := mustController(t, configuration, nil, nil)
	selection := mustSelect(t, controller, event)
	if selection.Tier != "pinned" || selection.RuleID != "exact-later" || selection.Channel != 7 {
		t.Fatalf("Evaluate() = %#v, want exact pinned rule", selection)
	}
	if _, err := controller.ReplaceMute(map[string]struct{}{flowID: {}}); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	selection = mustSelect(t, controller, event)
	if selection.Tier != "temporary_mute" || selection.State != flow.StateIgnore {
		t.Fatalf("muted Evaluate() = %#v", selection)
	}

	seenPersistent := 0
	safety := func(rules []flow.Rule) []flow.Rule {
		seenPersistent = len(rules)
		return []flow.Rule{{ID: "safety", Enabled: true, Match: flow.Match{ExactFlowID: flowID}, Action: flow.Action{State: flow.StateIgnore}}}
	}
	if err := controller.configureSafety(safety); err != nil {
		t.Fatalf("configureSafety() error = %v", err)
	}
	if seenPersistent != len(configuration.Rules) {
		t.Fatalf("safety callback saw %d persistent rules, want %d", seenPersistent, len(configuration.Rules))
	}
	selection = mustSelect(t, controller, event)
	if selection.Tier != "safety" || selection.RuleID != "safety" {
		t.Fatalf("safety Evaluate() = %#v", selection)
	}
}

func TestControllerRepositoryIsAuthoritativeAndCurrentIsDetached(t *testing.T) {
	stored := testConfig()
	stored.Mapping.DefaultChannel = 9
	repository := newMemoryConfigRepository(stored)
	provided := testConfig()
	provided.Mapping.DefaultChannel = 3
	provided.Instance.ID = "provided-is-not-authoritative"
	controller := mustController(t, provided, repository, nil)

	document := controller.Current()
	if document.Config.Mapping.DefaultChannel != 9 || document.Config.Instance.ID == provided.Instance.ID {
		t.Fatalf("Current() = %#v, want repository configuration", document.Config)
	}
	document.Config.Mapping.DefaultChannel = 15
	if controller.Current().Config.Mapping.DefaultChannel != 9 {
		t.Fatal("Current() leaked a mutable configuration alias")
	}
}

func TestControllerRejectsInvalidAuthoritativeRepositorySnapshot(t *testing.T) {
	repository := newMemoryConfigRepository(testConfig())
	repository.snapshot.Config.Instance.ID = ""
	if _, err := newController(testConfig(), repository, nil); err == nil || !strings.Contains(err.Error(), "instance.id") {
		t.Fatalf("newController() error = %v, want authoritative validation failure", err)
	}
}

func TestControllerClassifiesOnlyRulesAndMappingDefaultsAsHot(t *testing.T) {
	base := testConfig()
	controller := mustController(t, base, nil, nil)
	hot := base.Clone()
	hot.Mapping.DefaultState = config.FlowPlay
	hot.Mapping.DefaultChannel = 2
	hot.Rules = config.RulesConfig{{ID: "hot", Enabled: true, Action: config.RuleActionConfig{State: config.FlowMonitor}}}
	classification, err := controller.Validate(hot)
	if err != nil {
		t.Fatalf("Validate(hot) error = %v", err)
	}
	if len(classification.RestartRequiredFields) != 0 || !reflect.DeepEqual(classification.HotFields, []string{"mapping.default_channel", "mapping.default_state", "rules"}) {
		t.Fatalf("Validate(hot) = %#v", classification)
	}

	tests := []struct {
		field  string
		mutate func(*config.Config)
	}{
		{"instance.id", func(value *config.Config) { value.Instance.ID = "other" }},
		{"instance.role", func(value *config.Config) { value.Instance.Role = config.RoleHost }},
		{"capture.enabled", func(value *config.Config) { value.Capture.Enabled = false }},
		{"capture.interface", func(value *config.Config) { value.Capture.Interface = "other0" }},
		{"capture.bpf", func(value *config.Config) { value.Capture.BPF = "tcp" }},
		{"capture.snapshot_length", func(value *config.Config) { value.Capture.SnapshotLength-- }},
		{"capture.promiscuous", func(value *config.Config) { value.Capture.Promiscuous = !value.Capture.Promiscuous }},
		{"mapping.seed", func(value *config.Config) { value.Mapping.Seed = "other" }},
		{"mapping.minimum_note", func(value *config.Config) { value.Mapping.MinimumNote++ }},
		{"performance.packet_queue_capacity", func(value *config.Config) { value.Performance.PacketQueueCapacity++ }},
		{"midi.exact_device_name", func(value *config.Config) { value.MIDI.ExactDeviceName = "other" }},
		{"server.read_timeout", func(value *config.Config) { value.Server.ReadTimeout++ }},
		{"peer.stale_after", func(value *config.Config) { value.Peer.StaleAfter++ }},
		{"metrics.namespace", func(value *config.Config) { value.Metrics.Namespace = "other_namespace" }},
		{"logging.level", func(value *config.Config) { value.Logging.Level = config.LogLevelDebug }},
	}
	for _, test := range tests {
		t.Run(test.field, func(t *testing.T) {
			candidate := base.Clone()
			test.mutate(&candidate)
			classification, err := controller.Validate(candidate)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !containsString(classification.RestartRequiredFields, test.field) {
				t.Fatalf("restart fields = %v, want %q", classification.RestartRequiredFields, test.field)
			}
			_, err = controller.Update(controller.Current().Revision, candidate)
			var restart *RestartRequiredError
			if !errors.As(err, &restart) || !containsString(restart.Fields, test.field) {
				t.Fatalf("Update() error = %v, want restart-required %q", err, test.field)
			}
		})
	}
}

func TestControllerRejectsReadOnlyStaleAndPersistenceFailureWithoutMutation(t *testing.T) {
	configuration := testConfig()
	readOnly := mustController(t, configuration, nil, nil)
	current := readOnly.Current()
	candidate := current.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay
	if _, err := readOnly.Update("stale", candidate); !isConfigConflict(err) {
		t.Fatalf("Update(stale) error = %v, want conflict", err)
	}
	if _, err := readOnly.Update(current.Revision, candidate); !isReadOnly(err) {
		t.Fatalf("Update(read-only) error = %v, want *ReadOnlyError", err)
	}
	assertDocumentUnchanged(t, readOnly.Current(), current)

	repository := newMemoryConfigRepository(configuration)
	persistErr := errors.New("disk unavailable")
	repository.replaceErr = persistErr
	controller := mustController(t, configuration, repository, nil)
	current = controller.Current()
	if _, err := controller.Update(current.Revision, candidate); !errors.Is(err, persistErr) {
		t.Fatalf("Update(persist failure) error = %v, want %v", err, persistErr)
	}
	assertDocumentUnchanged(t, controller.Current(), current)
}

func TestControllerHotUpdateAndNoOpPreserveOverlay(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	flowID := strings.Repeat("a", 24)
	input := map[string]struct{}{flowID: {}}
	if _, err := controller.ReplaceMute(input); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	delete(input, flowID)
	current := controller.Current()
	candidate := current.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay
	candidate.Mapping.DefaultChannel = 6
	updated, err := controller.Update(current.Revision, candidate)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Revision == current.Revision {
		t.Fatal("Update() preserved revision for a changed configuration")
	}
	if _, found := controller.Overlay().Muted[flowID]; !found {
		t.Fatal("Update() lost the mute overlay")
	}
	unchanged, err := controller.Update(updated.Revision, updated.Config)
	if err != nil {
		t.Fatalf("Update(no-op) error = %v", err)
	}
	if unchanged.Revision != updated.Revision {
		t.Fatalf("no-op revision = %q, want %q", unchanged.Revision, updated.Revision)
	}

	overlay := controller.Overlay()
	delete(overlay.Muted, flowID)
	if _, found := controller.Overlay().Muted[flowID]; !found {
		t.Fatal("Overlay() leaked an internal map")
	}
}

func TestControllerOverlayIsValidatedBoundedAndCopyOnWrite(t *testing.T) {
	configuration := testConfig()
	configuration.Performance.FlowRegistryCapacity = 2
	controller := mustController(t, configuration, nil, nil)
	first := strings.Repeat("1", 24)
	second := strings.Repeat("2", 24)
	if _, err := controller.ReplaceMute(map[string]struct{}{first: {}, second: {}}); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	if _, err := controller.ReplaceSolo(map[string]struct{}{first: {}, second: {}, strings.Repeat("3", 24): {}}); err == nil {
		t.Fatal("ReplaceSolo() error = nil, want capacity error")
	}
	if _, err := controller.ReplaceSolo(map[string]struct{}{"INVALID": {}}); err == nil {
		t.Fatal("ReplaceSolo() error = nil, want flow-ID validation")
	}
	if got := len(controller.Overlay().Muted); got != 2 {
		t.Fatalf("mute count after rejected updates = %d, want 2", got)
	}
}

func TestControllerApplyFailureRollsBackExactFile(t *testing.T) {
	configuration := testConfig()
	repository, snapshot, path := writePolicyRepository(t, configuration, "# exact original\n")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	controller := mustController(t, configuration, repository, nil)
	applyErr := errors.New("activate failed")
	controller.apply = func(*policySnapshot) error { return applyErr }
	candidate := snapshot.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay

	_, err = controller.Update(snapshot.Revision, candidate)
	var policyErr *policyApplyError
	if !errors.As(err, &policyErr) || !errors.Is(err, applyErr) || policyErr.rollback != nil {
		t.Fatalf("Update() error = %v, want apply failure with successful rollback", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != string(original) {
		t.Fatalf("rollback contents = %q, want exact %q", contents, original)
	}
	current := controller.Current()
	if current.Revision != snapshot.Revision || current.State != ControllerStateReady || current.Warning != "" {
		t.Fatalf("Current() after rollback = %#v", current)
	}
}

func TestControllerApplyRollbackWarningAndFailurePublishHealthOnly(t *testing.T) {
	configuration := testConfig()
	applyErr := errors.New("activate failed")
	t.Run("warning", func(t *testing.T) {
		repository := newMemoryConfigRepository(configuration)
		rollbackWarning := errors.New("rollback directory sync uncertain")
		repository.rollbackWarning = rollbackWarning
		controller := mustController(t, configuration, repository, nil)
		before := controller.Current()
		controller.apply = func(*policySnapshot) error { return applyErr }
		candidate := before.Config.Clone()
		candidate.Mapping.DefaultState = config.FlowPlay
		_, err := controller.Update(before.Revision, candidate)
		if !errors.Is(err, applyErr) || !errors.Is(err, rollbackWarning) {
			t.Fatalf("Update() error = %v", err)
		}
		after := controller.Current()
		if after.Revision != before.Revision || after.Config.Mapping.DefaultState != before.Config.Mapping.DefaultState || after.State != ControllerStateDurabilityUncertain || !strings.Contains(after.Warning, rollbackWarning.Error()) {
			t.Fatalf("Current() after rollback warning = %#v", after)
		}
	})
	t.Run("failure", func(t *testing.T) {
		repository := newMemoryConfigRepository(configuration)
		rollbackErr := errors.New("rollback failed")
		repository.rollbackErr = rollbackErr
		controller := mustController(t, configuration, repository, nil)
		before := controller.Current()
		controller.apply = func(*policySnapshot) error { return applyErr }
		candidate := before.Config.Clone()
		candidate.Mapping.DefaultState = config.FlowPlay
		_, err := controller.Update(before.Revision, candidate)
		if !errors.Is(err, applyErr) || !errors.Is(err, rollbackErr) {
			t.Fatalf("Update() error = %v", err)
		}
		if controller.Current().State != ControllerStateDegraded {
			t.Fatalf("state = %q, want degraded", controller.Current().State)
		}
		if _, err := controller.Update(before.Revision, candidate); !isDegraded(err) {
			t.Fatalf("second Update() error = %v, want *DegradedError", err)
		}
	})
}

func TestControllerPublishesCommittedDurabilityWarning(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	warning := errors.New("directory sync uncertain")
	repository.warning = warning
	controller := mustController(t, configuration, repository, nil)
	current := controller.Current()
	candidate := current.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay
	updated, err := controller.Update(current.Revision, candidate)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.State != ControllerStateDurabilityUncertain || !strings.Contains(updated.Warning, warning.Error()) {
		t.Fatalf("Update() = %#v, want committed durability warning", updated)
	}
	noOp, err := controller.Update(updated.Revision, updated.Config)
	if err != nil {
		t.Fatalf("Update(no-op) error = %v", err)
	}
	if noOp.State != ControllerStateDurabilityUncertain || noOp.Warning != updated.Warning {
		t.Fatalf("Update(no-op) = %#v, want durability warning preserved", noOp)
	}
	if selection := mustSelect(t, controller, testPacket(41000, 443, time.Unix(100, 0))); selection.State != flow.StatePlay {
		t.Fatalf("Evaluate() = %#v, want published candidate", selection)
	}
}

func TestControllerExternalDriftCanBeReconciled(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	active := controller.Current()
	external := configuration.Clone()
	external.Mapping.DefaultChannel = 4
	externalRevision := repository.externalReplace(external)
	candidate := configuration.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay

	_, err := controller.Update(active.Revision, candidate)
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) || conflict.Actual != externalRevision {
		t.Fatalf("Update(stale) error = %v, want actual %q", err, externalRevision)
	}
	if controller.Current().State != ControllerStateOutOfSync {
		t.Fatalf("state = %q, want out_of_sync", controller.Current().State)
	}
	secondExternal := external.Clone()
	secondExternal.Mapping.DefaultChannel = 5
	secondExternalRevision := repository.externalReplace(secondExternal)
	_, err = controller.Update(externalRevision, candidate)
	if !errors.As(err, &conflict) || conflict.Actual != secondExternalRevision {
		t.Fatalf("Update(stale reconciliation) error = %v, want actual %q", err, secondExternalRevision)
	}
	reconciled, err := controller.Update(secondExternalRevision, candidate)
	if err != nil {
		t.Fatalf("Update(reconcile) error = %v", err)
	}
	if reconciled.State != ControllerStateReady || reconciled.Config.Mapping.DefaultState != config.FlowPlay {
		t.Fatalf("reconciled = %#v", reconciled)
	}
}

func TestControllerMarksUnverifiablePersistenceFailureOutOfSync(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	repository.replaceErr = errors.New("externally invalid config")
	repository.readErr = errors.New("decode externally invalid config")
	current := controller.Current()
	candidate := current.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay
	if _, err := controller.Update(current.Revision, candidate); !errors.Is(err, repository.replaceErr) {
		t.Fatalf("Update() error = %v, want persistence failure", err)
	}
	if got := controller.Current(); got.State != ControllerStateOutOfSync || got.Revision != current.Revision || got.Config.Mapping.DefaultState != current.Config.Mapping.DefaultState {
		t.Fatalf("Current() = %#v, want old active policy marked out_of_sync", got)
	}
}

func TestControllerEvaluateDuringConcurrentSwaps(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	event := testPacket(41000, 443, time.Unix(100, 0))
	key, _ := flow.Canonicalize(event)
	flowID := key.ID(configuration.Mapping.Seed)
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		for iteration := 0; iteration < 100; iteration++ {
			current := controller.Current()
			candidate := current.Config.Clone()
			candidate.Mapping.DefaultState = config.FlowState([]config.FlowState{config.FlowMonitor, config.FlowPlay}[iteration%2])
			_, _ = controller.Update(current.Revision, candidate)
		}
	}()
	go func() {
		defer writers.Done()
		for iteration := 0; iteration < 100; iteration++ {
			muted := map[string]struct{}{}
			if iteration%2 == 0 {
				muted[flowID] = struct{}{}
			}
			_, _ = controller.ReplaceMute(muted)
		}
	}()
	for iteration := 0; iteration < 500; iteration++ {
		selection := mustSelect(t, controller, event)
		if selection.State != flow.StateMonitor && selection.State != flow.StatePlay && selection.State != flow.StateIgnore {
			t.Fatalf("Evaluate() = %#v, want complete generation", selection)
		}
	}
	writers.Wait()
}

func mustController(t *testing.T, configuration config.Config, repository ConfigRepository, safety func([]flow.Rule) []flow.Rule) *Controller {
	t.Helper()
	controller, err := newController(configuration, repository, safety)
	if err != nil {
		t.Fatalf("newController() error = %v", err)
	}
	return controller
}

func mustSelect(t *testing.T, controller *Controller, event packet.Event) flow.Selection {
	t.Helper()
	selection, err := controller.store.Evaluate(event, flow.Overlay{Muted: map[string]struct{}{strings.Repeat("f", 24): {}}})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	return selection
}

func writePolicyRepository(t *testing.T, configuration config.Config, prefix string) (*config.FileRepository, config.Snapshot, string) {
	t.Helper()
	encoded, err := config.Encode(configuration)
	if err != nil {
		t.Fatalf("config.Encode() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, append([]byte(prefix), encoded...), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	repository, err := config.NewFileRepository(path)
	if err != nil {
		t.Fatalf("NewFileRepository() error = %v", err)
	}
	snapshot, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	return repository, snapshot, path
}

type memoryConfigRepository struct {
	mu              sync.Mutex
	snapshot        config.Snapshot
	replaceErr      error
	rollbackErr     error
	warning         error
	rollbackWarning error
	readErr         error
}

func newMemoryConfigRepository(configuration config.Config) *memoryConfigRepository {
	return &memoryConfigRepository{snapshot: memorySnapshot(configuration)}
}

func memorySnapshot(configuration config.Config) config.Snapshot {
	encoded, _ := config.Encode(configuration)
	return config.Snapshot{Config: configuration.Clone(), Revision: config.RevisionOf(encoded)}
}

func (repository *memoryConfigRepository) Read() (config.Snapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.readErr != nil {
		return config.Snapshot{}, repository.readErr
	}
	return config.Snapshot{Config: repository.snapshot.Config.Clone(), Revision: repository.snapshot.Revision}, nil
}

func (repository *memoryConfigRepository) Replace(expected config.Revision, candidate config.Config) (config.Change, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.replaceErr != nil {
		return config.Change{}, repository.replaceErr
	}
	if expected != repository.snapshot.Revision {
		return config.Change{}, &config.ConflictError{Expected: expected, Actual: repository.snapshot.Revision}
	}
	before := config.Snapshot{Config: repository.snapshot.Config.Clone(), Revision: repository.snapshot.Revision}
	if reflect.DeepEqual(before.Config, candidate) {
		return config.Change{Before: before, After: before}, nil
	}
	after := memorySnapshot(candidate)
	repository.snapshot = after
	return config.Change{Before: before, After: after, DurabilityWarning: repository.warning}, nil
}

func (repository *memoryConfigRepository) Rollback(change config.Change) (config.Change, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.rollbackErr != nil {
		return config.Change{}, repository.rollbackErr
	}
	before := repository.snapshot
	repository.snapshot = config.Snapshot{Config: change.Before.Config.Clone(), Revision: change.Before.Revision}
	return config.Change{Before: before, After: repository.snapshot, DurabilityWarning: repository.rollbackWarning}, nil
}

func (repository *memoryConfigRepository) externalReplace(configuration config.Config) config.Revision {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.snapshot = memorySnapshot(configuration)
	return repository.snapshot.Revision
}

func assertDocumentUnchanged(t *testing.T, got, want Document) {
	t.Helper()
	if got.Revision != want.Revision || !reflect.DeepEqual(got.Config, want.Config) || got.State != want.State || got.Warning != want.Warning {
		t.Fatalf("document changed: got %#v, want %#v", got, want)
	}
}

func isConfigConflict(err error) bool {
	var conflict *config.ConflictError
	return errors.As(err, &conflict)
}

func isReadOnly(err error) bool {
	var readOnly *ReadOnlyError
	return errors.As(err, &readOnly)
}

func isDegraded(err error) bool {
	var degraded *DegradedError
	return errors.As(err, &degraded)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
