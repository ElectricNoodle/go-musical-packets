package config

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestRulesConfigToFlowRulesPreservesOrderAndAllFields(t *testing.T) {
	rules := RulesConfig{
		{
			ID:      "first",
			Name:    "HTTPS SYN packets",
			Enabled: true,
			Match: RuleMatchConfig{
				ExactFlowID:      "0123456789abcdef01234567",
				SourceCIDR:       "192.0.2.42/24",
				DestinationCIDR:  "2001:db8::1/48",
				Protocol:         packet.ProtocolTCP,
				SourcePorts:      &PortRangeConfig{Minimum: 1024, Maximum: 65535},
				DestinationPorts: &PortRangeConfig{Minimum: 443, Maximum: 443},
				WireSize:         &SizeRangeConfig{Minimum: 64, Maximum: 1500},
				RequiredTCPFlags: []TCPFlag{TCPFlagSYN, TCPFlagACK},
			},
			Action: RuleActionConfig{State: FlowPlay, Channel: 6},
		},
		{
			ID:      "second",
			Enabled: false,
			Action:  RuleActionConfig{State: FlowMonitor},
		},
	}

	got, err := rules.ToFlowRules()
	if err != nil {
		t.Fatalf("ToFlowRules() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" {
		t.Fatalf("ToFlowRules() order = %#v", got)
	}
	first := got[0]
	if first.Name != rules[0].Name || !first.Enabled || first.Match.ExactFlowID != rules[0].Match.ExactFlowID {
		t.Fatalf("ToFlowRules()[0] identity = %#v", first)
	}
	if first.Match.SourcePrefix == nil || *first.Match.SourcePrefix != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatalf("source prefix = %v", first.Match.SourcePrefix)
	}
	if first.Match.DestinationPrefix == nil || *first.Match.DestinationPrefix != netip.MustParsePrefix("2001:db8::/48") {
		t.Fatalf("destination prefix = %v", first.Match.DestinationPrefix)
	}
	if first.Match.Protocol != packet.ProtocolTCP || first.Match.SourcePorts == nil || *first.Match.SourcePorts != (flow.PortRange{Minimum: 1024, Maximum: 65535}) {
		t.Fatalf("protocol/source ports = %q, %#v", first.Match.Protocol, first.Match.SourcePorts)
	}
	if first.Match.DestinationPorts == nil || *first.Match.DestinationPorts != (flow.PortRange{Minimum: 443, Maximum: 443}) {
		t.Fatalf("destination ports = %#v", first.Match.DestinationPorts)
	}
	if first.Match.WireSize == nil || *first.Match.WireSize != (flow.SizeRange{Minimum: 64, Maximum: 1500}) {
		t.Fatalf("wire size = %#v", first.Match.WireSize)
	}
	if first.Match.RequiredTCPFlags != packet.TCPFlagSYN|packet.TCPFlagACK {
		t.Fatalf("TCP flags = %b", first.Match.RequiredTCPFlags)
	}
	if first.Action != (flow.Action{State: flow.StatePlay, Channel: 6}) {
		t.Fatalf("action = %#v", first.Action)
	}
	if got[1].Action.Channel != 0 {
		t.Fatalf("inherited channel = %d, want 0", got[1].Action.Channel)
	}
}

func TestRulesConfigToFlowRulesRejectsInvalidRules(t *testing.T) {
	valid := func() RuleConfig {
		return RuleConfig{ID: "valid", Enabled: true, Action: RuleActionConfig{State: FlowMonitor}}
	}
	tests := []struct {
		name   string
		mutate func(*RuleConfig)
		want   string
	}{
		{name: "missing ID", mutate: func(rule *RuleConfig) { rule.ID = " " }, want: "id is required"},
		{name: "bad action state", mutate: func(rule *RuleConfig) { rule.Action.State = "dance" }, want: "action.state"},
		{name: "bad action channel", mutate: func(rule *RuleConfig) { rule.Action.Channel = 17 }, want: "action.channel"},
		{name: "short exact flow ID", mutate: func(rule *RuleConfig) { rule.Match.ExactFlowID = "0123" }, want: "match.exact_flow_id"},
		{name: "uppercase exact flow ID", mutate: func(rule *RuleConfig) { rule.Match.ExactFlowID = "0123456789ABCDEF01234567" }, want: "match.exact_flow_id"},
		{name: "bad protocol", mutate: func(rule *RuleConfig) { rule.Match.Protocol = "sctp" }, want: "match.protocol"},
		{name: "bad source CIDR", mutate: func(rule *RuleConfig) { rule.Match.SourceCIDR = "192.0.2.0/99" }, want: "match.source_cidr"},
		{name: "bad destination CIDR", mutate: func(rule *RuleConfig) { rule.Match.DestinationCIDR = "example.com" }, want: "match.destination_cidr"},
		{name: "reversed source ports", mutate: func(rule *RuleConfig) { rule.Match.SourcePorts = &PortRangeConfig{Minimum: 2, Maximum: 1} }, want: "match.source_ports"},
		{name: "reversed destination ports", mutate: func(rule *RuleConfig) { rule.Match.DestinationPorts = &PortRangeConfig{Minimum: 2, Maximum: 1} }, want: "match.destination_ports"},
		{name: "negative wire size", mutate: func(rule *RuleConfig) { rule.Match.WireSize = &SizeRangeConfig{Minimum: -1, Maximum: 10} }, want: "match.wire_size"},
		{name: "reversed wire size", mutate: func(rule *RuleConfig) { rule.Match.WireSize = &SizeRangeConfig{Minimum: 10, Maximum: 9} }, want: "match.wire_size"},
		{name: "unknown TCP flag", mutate: func(rule *RuleConfig) { rule.Match.RequiredTCPFlags = []TCPFlag{"ece"} }, want: "required_tcp_flags"},
		{name: "duplicate TCP flag", mutate: func(rule *RuleConfig) { rule.Match.RequiredTCPFlags = []TCPFlag{TCPFlagSYN, TCPFlagSYN} }, want: "duplicated"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := valid()
			test.mutate(&rule)
			_, err := (RulesConfig{rule}).ToFlowRules()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ToFlowRules() error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestRulesConfigToFlowRulesRejectsDuplicateIDs(t *testing.T) {
	rules := RulesConfig{
		{ID: "same", Action: RuleActionConfig{State: FlowIgnore}},
		{ID: "same", Action: RuleActionConfig{State: FlowPlay}},
	}
	if _, err := rules.ToFlowRules(); err == nil || !strings.Contains(err.Error(), "duplicates rules[0].id") {
		t.Fatalf("ToFlowRules() error = %v, want duplicate ID", err)
	}
}

func TestValidateIncludesRuleProblems(t *testing.T) {
	config := Default()
	config.Rules = RulesConfig{{ID: "bad", Action: RuleActionConfig{State: FlowPlay, Channel: 17}}}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "rules[0]") {
		t.Fatalf("Validate() error = %v, want indexed rule error", err)
	}
}
