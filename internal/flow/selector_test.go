package flow

import (
	"net/netip"
	"sync"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestSelectorRuleOrderAndChannel(t *testing.T) {
	event := selectorEvent()
	tcp := packet.ProtocolTCP
	selector := mustSelector(t, SelectorConfig{
		Seed:    "seed",
		Default: Action{State: StateMonitor, Channel: 1},
		UserRules: []Rule{
			{ID: "first", Enabled: true, Match: Match{Protocol: tcp}, Action: Action{State: StatePlay, Channel: 4}},
			{ID: "second", Enabled: true, Match: Match{Protocol: tcp}, Action: Action{State: StatePlay, Channel: 9}},
		},
	})

	got, err := selector.Evaluate(event, Overlay{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.RuleID != "first" || got.Channel != 4 || got.State != StatePlay || got.Tier != "user" {
		t.Fatalf("Evaluate() = %#v", got)
	}
}

func TestSelectorPropagatesFixedMusicalIdentity(t *testing.T) {
	selector := mustSelector(t, SelectorConfig{
		Seed: "seed", Default: Action{State: StateMonitor, Channel: 1},
		UserRules: []Rule{{
			ID: "harmonic-filter", Enabled: true, Match: Match{Protocol: packet.ProtocolTCP},
			Action: Action{State: StatePlay, Channel: 5, Mode: "dorian", Root: 2},
		}},
	})
	got, err := selector.Evaluate(selectorEvent(), Overlay{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Mode != "dorian" || got.Root != 2 || got.Channel != 5 {
		t.Fatalf("selection = %#v, want fixed D dorian on channel 5", got)
	}
}

func TestSelectorPrecedence(t *testing.T) {
	event := selectorEvent()
	key, _ := Canonicalize(event)
	flowID := key.ID("seed")
	selector := mustSelector(t, SelectorConfig{
		Seed:    "seed",
		Default: Action{State: StateMonitor, Channel: 2},
		SafetyRules: []Rule{
			{ID: "control-plane", Enabled: true, Match: Match{DestinationPorts: &PortRange{Minimum: 9090, Maximum: 9090}}, Action: Action{State: StateIgnore}},
		},
		PinnedRules: []Rule{
			{ID: "pinned", Enabled: true, Match: Match{ExactFlowID: flowID}, Action: Action{State: StatePlay, Channel: 6}},
		},
	})

	got, err := selector.Evaluate(event, Overlay{})
	if err != nil {
		t.Fatalf("Evaluate(pinned) error = %v", err)
	}
	if got.Tier != "pinned" || got.Channel != 6 {
		t.Fatalf("pinned selection = %#v", got)
	}

	got, err = selector.Evaluate(event, Overlay{Muted: map[string]struct{}{flowID: {}}})
	if err != nil {
		t.Fatalf("Evaluate(muted) error = %v", err)
	}
	if got.Tier != "temporary_mute" || got.State != StateIgnore {
		t.Fatalf("muted selection = %#v", got)
	}

	control := event
	control.Destination.Port = 9090
	controlKey, _ := Canonicalize(control)
	controlID := controlKey.ID("seed")
	got, err = selector.Evaluate(control, Overlay{Soloed: map[string]struct{}{controlID: {}}})
	if err != nil {
		t.Fatalf("Evaluate(safety) error = %v", err)
	}
	if got.Tier != "safety" || got.State != StateIgnore {
		t.Fatalf("safety selection = %#v", got)
	}
}

func TestSelectorSoloMonitorsOtherFlows(t *testing.T) {
	event := selectorEvent()
	selector := mustSelector(t, SelectorConfig{
		Seed:    "seed",
		Default: Action{State: StatePlay, Channel: 3},
	})

	got, err := selector.Evaluate(event, Overlay{Soloed: map[string]struct{}{"different-flow": {}}})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.State != StateMonitor || got.Tier != "temporary_solo" || got.Channel != 3 {
		t.Fatalf("solo exclusion = %#v", got)
	}
}

func TestSelectorMatchesCIDRAndPort(t *testing.T) {
	prefix := netip.MustParsePrefix("198.51.100.0/24")
	selector := mustSelector(t, SelectorConfig{
		Seed:    "seed",
		Default: Action{State: StateMonitor, Channel: 1},
		UserRules: []Rule{{
			ID:      "web",
			Enabled: true,
			Match: Match{
				DestinationPrefix: &prefix,
				DestinationPorts:  &PortRange{Minimum: 443, Maximum: 443},
			},
			Action: Action{State: StatePlay},
		}},
	})

	got, err := selector.Evaluate(selectorEvent(), Overlay{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.RuleID != "web" || got.Channel != 1 {
		t.Fatalf("selection = %#v", got)
	}
}

func TestSelectorClonesCallerOwnedRulesAndMatchPointers(t *testing.T) {
	event := selectorEvent()
	key, _ := Canonicalize(event)
	flowID := key.ID("seed")
	sourcePrefix := netip.MustParsePrefix("192.0.2.0/24")
	destinationPrefix := netip.MustParsePrefix("198.51.100.0/24")
	sourcePorts := PortRange{Minimum: 50000, Maximum: 50000}
	destinationPorts := PortRange{Minimum: 443, Maximum: 443}
	wireSize := SizeRange{Minimum: 512, Maximum: 512}
	safetyRules := []Rule{{
		ID:      "safety",
		Enabled: false,
		Match:   Match{ExactFlowID: "not-this-flow"},
		Action:  Action{State: StateIgnore},
	}}
	pinnedRules := []Rule{{
		ID:      "pinned",
		Enabled: false,
		Match:   Match{ExactFlowID: "not-this-flow"},
		Action:  Action{State: StatePlay, Channel: 9},
	}}
	userRules := []Rule{{
		ID:      "immutable",
		Enabled: true,
		Match: Match{
			ExactFlowID:       flowID,
			SourcePrefix:      &sourcePrefix,
			DestinationPrefix: &destinationPrefix,
			Protocol:          packet.ProtocolTCP,
			SourcePorts:       &sourcePorts,
			DestinationPorts:  &destinationPorts,
			WireSize:          &wireSize,
			RequiredTCPFlags:  packet.TCPFlagACK,
		},
		Action: Action{State: StatePlay, Channel: 7},
	}}

	selector := mustSelector(t, SelectorConfig{
		Seed:        "seed",
		Default:     Action{State: StateMonitor, Channel: 1},
		SafetyRules: safetyRules,
		PinnedRules: pinnedRules,
		UserRules:   userRules,
	})

	assertImmutableSelection := func() {
		t.Helper()
		got, err := selector.Evaluate(event, Overlay{})
		if err != nil {
			t.Errorf("Evaluate() error = %v", err)
			return
		}
		want := Selection{FlowID: flowID, State: StatePlay, Channel: 7, RuleID: "immutable", Tier: "user"}
		if got != want {
			t.Errorf("Evaluate() = %#v, want %#v", got, want)
		}
	}
	assertImmutableSelection()

	mutate := func(iteration int) {
		if iteration%2 == 0 {
			sourcePrefix = netip.MustParsePrefix("203.0.113.0/24")
			destinationPrefix = netip.MustParsePrefix("203.0.113.0/24")
			sourcePorts = PortRange{Minimum: 1, Maximum: 2}
			destinationPorts = PortRange{Minimum: 3, Maximum: 4}
			wireSize = SizeRange{Minimum: 1, Maximum: 2}
		} else {
			sourcePrefix = netip.MustParsePrefix("2001:db8::/32")
			destinationPrefix = netip.MustParsePrefix("2001:db8:1::/48")
			sourcePorts = PortRange{Minimum: 5, Maximum: 6}
			destinationPorts = PortRange{Minimum: 7, Maximum: 8}
			wireSize = SizeRange{Minimum: 3, Maximum: 4}
		}
		safetyRules[0].Enabled = true
		safetyRules[0].Match.ExactFlowID = flowID
		pinnedRules[0].Enabled = true
		pinnedRules[0].Match.ExactFlowID = flowID
		userRules[0].ID = "mutated"
		userRules[0].Enabled = false
		userRules[0].Action = Action{State: StateIgnore, Channel: 16}
	}

	var mutations sync.WaitGroup
	mutations.Add(1)
	go func() {
		defer mutations.Done()
		for iteration := 0; iteration < 1_000; iteration++ {
			mutate(iteration)
		}
	}()
	for iteration := 0; iteration < 1_000; iteration++ {
		assertImmutableSelection()
	}
	mutations.Wait()
	assertImmutableSelection()
}

func TestNewSelectorRejectsInvalidRules(t *testing.T) {
	_, err := NewSelector(SelectorConfig{
		Seed:    "seed",
		Default: Action{State: StateMonitor, Channel: 1},
		SafetyRules: []Rule{{
			ID:      "unsafe-safety",
			Enabled: true,
			Action:  Action{State: StatePlay, Channel: 1},
		}},
	})
	if err == nil {
		t.Fatal("NewSelector() error = nil")
	}
}

func TestNewSelectorRejectsInvalidFixedMusicalIdentity(t *testing.T) {
	tests := []Action{
		{State: StatePlay, Mode: "unknown"},
		{State: StatePlay, Mode: "dorian", Root: 12},
		{State: StateMonitor, Mode: "dorian", Root: 2},
		{State: StatePlay, Root: 2},
	}
	for _, action := range tests {
		_, err := NewSelector(SelectorConfig{
			Seed: "seed", Default: Action{State: StateMonitor, Channel: 1},
			UserRules: []Rule{{ID: "invalid", Enabled: true, Action: action}},
		})
		if err == nil {
			t.Fatalf("NewSelector(%#v) error = nil", action)
		}
	}
}

func mustSelector(t *testing.T, config SelectorConfig) *Selector {
	t.Helper()
	selector, err := NewSelector(config)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	return selector
}

func selectorEvent() packet.Event {
	return packet.Event{
		Source:         packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: 50000},
		Destination:    packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: 443},
		Protocol:       packet.ProtocolTCP,
		WireLength:     512,
		CapturedLength: 512,
		PayloadLength:  472,
		TCPFlags:       packet.TCPFlagACK,
	}
}
