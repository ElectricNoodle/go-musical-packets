package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
)

func TestControllerMutateChecksExpectedRevisionBeforeCallback(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	before := controller.Current()
	callbackErr := errors.New("callback must not run")
	called := false

	_, err := controller.Mutate("stale", func(*config.Config) error {
		called = true
		return callbackErr
	})
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) || errors.Is(err, callbackErr) {
		t.Fatalf("Mutate(stale) error = %v, want only revision conflict", err)
	}
	if called {
		t.Fatal("Mutate(stale) invoked mutation callback")
	}
	assertDocumentUnchanged(t, controller.Current(), before)
	durable, readErr := repository.Read()
	if readErr != nil {
		t.Fatalf("Read() error = %v", readErr)
	}
	if durable.Revision != before.Revision || !reflect.DeepEqual(durable.Config, before.Config) {
		t.Fatalf("durable configuration changed: got %#v, want %#v", durable, before)
	}
}

func TestControllerMutateRejectsNilAndCanceledInputs(t *testing.T) {
	configuration := testConfig()

	tests := []struct {
		name string
		run  func(*Controller, config.Revision, *atomic.Int32) error
		want error
	}{
		{
			name: "nil mutation",
			run: func(controller *Controller, revision config.Revision, _ *atomic.Int32) error {
				_, err := controller.Mutate(revision, nil)
				return err
			},
		},
		{
			name: "nil context",
			run: func(controller *Controller, revision config.Revision, called *atomic.Int32) error {
				_, err := controller.MutateContext(nil, revision, func(*config.Config) error {
					called.Add(1)
					return nil
				})
				return err
			},
		},
		{
			name: "pre-canceled context",
			run: func(controller *Controller, revision config.Revision, called *atomic.Int32) error {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := controller.MutateContext(ctx, revision, func(*config.Config) error {
					called.Add(1)
					return nil
				})
				return err
			},
			want: context.Canceled,
		},
		{
			name: "canceled by callback",
			run: func(controller *Controller, revision config.Revision, called *atomic.Int32) error {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				_, err := controller.MutateContext(ctx, revision, func(candidate *config.Config) error {
					called.Add(1)
					candidate.Mapping.DefaultState = config.FlowPlay
					cancel()
					return nil
				})
				return err
			},
			want: context.Canceled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryConfigRepository(configuration)
			controller := mustController(t, configuration, repository, nil)
			before := controller.Current()
			var called atomic.Int32

			err := test.run(controller, before.Revision, &called)
			if err == nil {
				t.Fatal("Mutate() error = nil, want rejection")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Mutate() error = %v, want %v", err, test.want)
			}
			if test.name != "canceled by callback" && called.Load() != 0 {
				t.Fatalf("callback calls = %d, want 0", called.Load())
			}
			if test.name == "canceled by callback" && called.Load() != 1 {
				t.Fatalf("callback calls = %d, want 1", called.Load())
			}
			assertDocumentUnchanged(t, controller.Current(), before)
			durable, readErr := repository.Read()
			if readErr != nil {
				t.Fatalf("Read() error = %v", readErr)
			}
			if durable.Revision != before.Revision || !reflect.DeepEqual(durable.Config, before.Config) {
				t.Fatalf("durable configuration changed: got %#v, want %#v", durable, before)
			}
		})
	}
}

func TestControllerMutateCallbackErrorLeavesPolicyUntouched(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	mutedID := strings.Repeat("a", 24)
	if _, err := controller.ReplaceMute(map[string]struct{}{mutedID: {}}); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	before := controller.Current()
	beforeOverlay := controller.Overlay()
	callbackErr := errors.New("rule does not exist")

	_, err := controller.Mutate(before.Revision, func(candidate *config.Config) error {
		candidate.Mapping.DefaultState = config.FlowPlay
		candidate.Rules = append(candidate.Rules, mutationTestRule("discarded"))
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("Mutate() error = %v, want %v", err, callbackErr)
	}
	assertDocumentUnchanged(t, controller.Current(), before)
	if got := controller.Overlay(); !reflect.DeepEqual(got, beforeOverlay) {
		t.Fatalf("Overlay() = %#v, want %#v", got, beforeOverlay)
	}
	durable, readErr := repository.Read()
	if readErr != nil {
		t.Fatalf("Read() error = %v", readErr)
	}
	if durable.Revision != before.Revision || !reflect.DeepEqual(durable.Config, before.Config) {
		t.Fatalf("durable configuration changed: got %#v, want %#v", durable, before)
	}
}

func TestControllerMutateClonesCallbackInputAndOutput(t *testing.T) {
	configuration := testConfig()
	configuration.Rules = config.RulesConfig{{
		ID:      "nested",
		Enabled: true,
		Match: config.RuleMatchConfig{
			SourcePorts:      &config.PortRangeConfig{Minimum: 10, Maximum: 20},
			RequiredTCPFlags: []config.TCPFlag{config.TCPFlagSYN},
		},
		Action: config.RuleActionConfig{State: config.FlowMonitor},
	}}
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	before := controller.Current()
	var retained *config.Config

	updated, err := controller.Mutate(before.Revision, func(candidate *config.Config) error {
		retained = candidate
		candidate.Rules[0].Name = "committed"
		candidate.Rules[0].Match.SourcePorts.Minimum = 11
		candidate.Rules[0].Match.RequiredTCPFlags[0] = config.TCPFlagACK
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if retained == nil {
		t.Fatal("mutation callback did not retain its candidate")
	}

	retained.Mapping.DefaultState = config.FlowPlay
	retained.Rules[0].Name = "aliased-after-return"
	retained.Rules[0].Match.SourcePorts.Minimum = 19
	retained.Rules[0].Match.RequiredTCPFlags[0] = config.TCPFlagRST

	current := controller.Current()
	if current.Revision != updated.Revision || current.Config.Mapping.DefaultState != before.Config.Mapping.DefaultState {
		t.Fatalf("Current() after retained mutation = %#v", current)
	}
	rule := current.Config.Rules[0]
	if rule.Name != "committed" || rule.Match.SourcePorts.Minimum != 11 || !reflect.DeepEqual(rule.Match.RequiredTCPFlags, []config.TCPFlag{config.TCPFlagACK}) {
		t.Fatalf("Current() rule after retained mutation = %#v", rule)
	}
	durable, readErr := repository.Read()
	if readErr != nil {
		t.Fatalf("Read() error = %v", readErr)
	}
	if !reflect.DeepEqual(durable.Config, current.Config) {
		t.Fatalf("durable configuration = %#v, want %#v", durable.Config, current.Config)
	}
}

func TestControllerMutatePreservesOverlayAndPublishesRulesImmediately(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	event := testPacket(41000, 443, time.Unix(100, 0))
	key, _ := flow.Canonicalize(event)
	flowID := key.ID(configuration.Mapping.Seed)
	mutedID := strings.Repeat("a", 24)
	if mutedID == flowID {
		mutedID = strings.Repeat("b", 24)
	}
	if _, err := controller.ReplaceMute(map[string]struct{}{mutedID: {}}); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	before := controller.Current()

	updated, err := controller.Mutate(before.Revision, func(candidate *config.Config) error {
		rule := mutationTestRule("immediate")
		rule.Match.ExactFlowID = flowID
		rule.Action = config.RuleActionConfig{State: config.FlowPlay, Channel: 7}
		candidate.Rules = append(candidate.Rules, rule)
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if updated.Revision == before.Revision {
		t.Fatal("Mutate() preserved revision for a changed rule set")
	}
	if _, found := controller.Overlay().Muted[mutedID]; !found {
		t.Fatal("Mutate() lost the mute overlay")
	}
	selection := mustSelect(t, controller, event)
	if selection.Tier != "pinned" || selection.RuleID != "immediate" || selection.State != flow.StatePlay || selection.Channel != 7 {
		t.Fatalf("Evaluate() = %#v, want immediately published pinned rule", selection)
	}
}

func TestControllerConcurrentMutationsAtSameRevisionHaveOneWinner(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	expected := controller.Current().Revision
	start := make(chan struct{})
	type result struct {
		document Document
		err      error
	}
	results := make(chan result, 2)
	var callbacks atomic.Int32
	var writers sync.WaitGroup

	for index := 0; index < 2; index++ {
		channel := uint8(index + 2)
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			document, err := controller.Mutate(expected, func(candidate *config.Config) error {
				callbacks.Add(1)
				candidate.Mapping.DefaultChannel = channel
				return nil
			})
			results <- result{document: document, err: err}
		}()
	}
	close(start)
	writers.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for outcome := range results {
		if outcome.err == nil {
			successes++
			continue
		}
		var conflict *config.ConflictError
		if !errors.As(outcome.err, &conflict) {
			t.Fatalf("Mutate() error = %v, want revision conflict", outcome.err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("outcomes = %d successes, %d conflicts; want one each", successes, conflicts)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("mutation callback calls = %d, want 1", callbacks.Load())
	}
	current := controller.Current()
	if current.Revision == expected || (current.Config.Mapping.DefaultChannel != 2 && current.Config.Mapping.DefaultChannel != 3) {
		t.Fatalf("winning document = %#v", current)
	}
	durable, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if durable.Revision != current.Revision || !reflect.DeepEqual(durable.Config, current.Config) {
		t.Fatalf("durable configuration = %#v, want active %#v", durable, current)
	}
}

func TestControllerMutateReconcilesFromDurableBase(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	active := controller.Current()
	external := configuration.Clone()
	external.Mapping.DefaultChannel = 4
	externalRevision := repository.externalReplace(external)

	_, err := controller.Mutate(active.Revision, func(candidate *config.Config) error {
		candidate.Rules = append(candidate.Rules, mutationTestRule("requested"))
		return nil
	})
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) || conflict.Actual != externalRevision {
		t.Fatalf("Mutate(first drift) error = %v, want actual %q", err, externalRevision)
	}
	if controller.Current().State != ControllerStateOutOfSync {
		t.Fatalf("state = %q, want out_of_sync", controller.Current().State)
	}

	seenChannel := uint8(0)
	reconciled, err := controller.Mutate(externalRevision, func(candidate *config.Config) error {
		seenChannel = candidate.Mapping.DefaultChannel
		candidate.Rules = append(candidate.Rules, mutationTestRule("requested"))
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate(reconcile) error = %v", err)
	}
	if seenChannel != external.Mapping.DefaultChannel {
		t.Fatalf("mutation base default channel = %d, want durable %d", seenChannel, external.Mapping.DefaultChannel)
	}
	if reconciled.State != ControllerStateReady || reconciled.Config.Mapping.DefaultChannel != external.Mapping.DefaultChannel {
		t.Fatalf("reconciled document = %#v", reconciled)
	}
	if len(reconciled.Config.Rules) != 1 || reconciled.Config.Rules[0].ID != "requested" {
		t.Fatalf("reconciled rules = %#v, want requested rule", reconciled.Config.Rules)
	}
	durable, readErr := repository.Read()
	if readErr != nil {
		t.Fatalf("Read() error = %v", readErr)
	}
	if durable.Revision != reconciled.Revision || !reflect.DeepEqual(durable.Config, reconciled.Config) {
		t.Fatalf("durable configuration = %#v, want reconciled %#v", durable, reconciled)
	}
}

func TestControllerMutateRejectsRestartRequiredDurableDrift(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	active := controller.Current()
	external := configuration.Clone()
	external.Server.ListenAddress = "127.0.0.1:43210"
	externalRevision := repository.externalReplace(external)

	_, err := controller.Mutate(active.Revision, func(candidate *config.Config) error {
		candidate.Rules = append(candidate.Rules, mutationTestRule("requested"))
		return nil
	})
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) || conflict.Actual != externalRevision {
		t.Fatalf("Mutate(first drift) error = %v, want actual %q", err, externalRevision)
	}

	seenListenAddress := ""
	_, err = controller.Mutate(externalRevision, func(candidate *config.Config) error {
		seenListenAddress = candidate.Server.ListenAddress
		candidate.Rules = append(candidate.Rules, mutationTestRule("requested"))
		return nil
	})
	var restart *RestartRequiredError
	if !errors.As(err, &restart) || !containsString(restart.Fields, "server.listen_address") {
		t.Fatalf("Mutate(reconcile restart drift) error = %v, want server.listen_address restart rejection", err)
	}
	if seenListenAddress != external.Server.ListenAddress {
		t.Fatalf("mutation base listen address = %q, want durable %q", seenListenAddress, external.Server.ListenAddress)
	}
	current := controller.Current()
	if current.State != ControllerStateOutOfSync || current.Revision != active.Revision || !reflect.DeepEqual(current.Config, active.Config) {
		t.Fatalf("Current() after restart rejection = %#v, want active out_of_sync document", current)
	}
	durable, readErr := repository.Read()
	if readErr != nil {
		t.Fatalf("Read() error = %v", readErr)
	}
	if durable.Revision != externalRevision || !reflect.DeepEqual(durable.Config, external) {
		t.Fatalf("durable configuration = %#v, want untouched external %#v", durable, external)
	}
}

func TestControllerMutateDetectsSecondDriftDuringReconciliation(t *testing.T) {
	configuration := testConfig()
	backing := newMemoryConfigRepository(configuration)
	repository := &mutationReadDriftRepository{memoryConfigRepository: backing}
	controller := mustController(t, configuration, repository, nil)
	active := controller.Current()
	firstExternal := configuration.Clone()
	firstExternal.Mapping.DefaultChannel = 4
	firstExternalRevision := backing.externalReplace(firstExternal)

	_, err := controller.Mutate(active.Revision, func(candidate *config.Config) error {
		candidate.Rules = append(candidate.Rules, mutationTestRule("requested"))
		return nil
	})
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) || conflict.Actual != firstExternalRevision {
		t.Fatalf("Mutate(first drift) error = %v, want actual %q", err, firstExternalRevision)
	}

	secondExternal := firstExternal.Clone()
	secondExternal.Mapping.DefaultChannel = 5
	secondExternalRevision := memorySnapshot(secondExternal).Revision
	repository.driftAfterNextRead(secondExternal)
	seenChannel := uint8(0)
	_, err = controller.Mutate(firstExternalRevision, func(candidate *config.Config) error {
		seenChannel = candidate.Mapping.DefaultChannel
		candidate.Rules = append(candidate.Rules, mutationTestRule("requested"))
		return nil
	})
	if !errors.As(err, &conflict) || conflict.Actual != secondExternalRevision {
		t.Fatalf("Mutate(second drift) error = %v, want actual %q", err, secondExternalRevision)
	}
	if seenChannel != firstExternal.Mapping.DefaultChannel {
		t.Fatalf("mutation base default channel = %d, want read revision channel %d", seenChannel, firstExternal.Mapping.DefaultChannel)
	}
	current := controller.Current()
	if current.State != ControllerStateOutOfSync || current.Revision != active.Revision || !reflect.DeepEqual(current.Config, active.Config) {
		t.Fatalf("Current() after second drift = %#v, want active out_of_sync document", current)
	}
	durable, readErr := backing.Read()
	if readErr != nil {
		t.Fatalf("Read() error = %v", readErr)
	}
	if durable.Revision != secondExternalRevision || !reflect.DeepEqual(durable.Config, secondExternal) {
		t.Fatalf("durable configuration = %#v, want second external %#v", durable, secondExternal)
	}
}

func mutationTestRule(id string) config.RuleConfig {
	return config.RuleConfig{
		ID:      id,
		Enabled: true,
		Action:  config.RuleActionConfig{State: config.FlowMonitor},
	}
}

type mutationReadDriftRepository struct {
	*memoryConfigRepository

	hookMu    sync.Mutex
	afterRead *config.Config
}

func (repository *mutationReadDriftRepository) Read() (config.Snapshot, error) {
	snapshot, err := repository.memoryConfigRepository.Read()
	if err != nil {
		return config.Snapshot{}, err
	}
	repository.hookMu.Lock()
	drift := repository.afterRead
	repository.afterRead = nil
	repository.hookMu.Unlock()
	if drift != nil {
		repository.memoryConfigRepository.externalReplace(drift.Clone())
	}
	return snapshot, nil
}

func (repository *mutationReadDriftRepository) driftAfterNextRead(candidate config.Config) {
	repository.hookMu.Lock()
	defer repository.hookMu.Unlock()
	clone := candidate.Clone()
	repository.afterRead = &clone
}
