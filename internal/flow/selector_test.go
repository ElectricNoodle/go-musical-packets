package flow

import (
	"net/netip"
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
