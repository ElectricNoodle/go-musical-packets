package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

// TCPFlag is a TCP control flag accepted by a configured rule.
type TCPFlag string

const (
	TCPFlagFIN TCPFlag = "fin"
	TCPFlagSYN TCPFlag = "syn"
	TCPFlagRST TCPFlag = "rst"
	TCPFlagPSH TCPFlag = "psh"
	TCPFlagACK TCPFlag = "ack"
	TCPFlagURG TCPFlag = "urg"
)

// RulesConfig is the ordered set of persistent user rules. First match wins.
type RulesConfig []RuleConfig

type RuleConfig struct {
	ID      string           `json:"id" yaml:"id"`
	Name    string           `json:"name" yaml:"name"`
	Enabled bool             `json:"enabled" yaml:"enabled"`
	Match   RuleMatchConfig  `json:"match" yaml:"match"`
	Action  RuleActionConfig `json:"action" yaml:"action"`
}

type RuleMatchConfig struct {
	ExactFlowID      string           `json:"exact_flow_id" yaml:"exact_flow_id"`
	SourceCIDR       string           `json:"source_cidr" yaml:"source_cidr"`
	DestinationCIDR  string           `json:"destination_cidr" yaml:"destination_cidr"`
	Protocol         packet.Protocol  `json:"protocol" yaml:"protocol"`
	SourcePorts      *PortRangeConfig `json:"source_ports,omitempty" yaml:"source_ports,omitempty"`
	DestinationPorts *PortRangeConfig `json:"destination_ports,omitempty" yaml:"destination_ports,omitempty"`
	WireSize         *SizeRangeConfig `json:"wire_size,omitempty" yaml:"wire_size,omitempty"`
	RequiredTCPFlags []TCPFlag        `json:"required_tcp_flags,omitempty" yaml:"required_tcp_flags,omitempty"`
}

type PortRangeConfig struct {
	Minimum uint16 `json:"minimum" yaml:"minimum"`
	Maximum uint16 `json:"maximum" yaml:"maximum"`
}

type SizeRangeConfig struct {
	Minimum int `json:"minimum" yaml:"minimum"`
	Maximum int `json:"maximum" yaml:"maximum"`
}

type RuleActionConfig struct {
	State   FlowState `json:"state" yaml:"state"`
	Channel uint8     `json:"channel" yaml:"channel"`
}

// FlowRules validates and converts the configured persistent user rules.
func (config Config) FlowRules() ([]flow.Rule, error) {
	return config.Rules.ToFlowRules()
}

// ToFlowRules validates and converts persistent rules without changing their
// order. A zero action channel retains the selector's inherit behavior.
func (rules RulesConfig) ToFlowRules() ([]flow.Rule, error) {
	converted := make([]flow.Rule, len(rules))
	seenIDs := make(map[string]int, len(rules))
	var problems []error

	for index, rule := range rules {
		if strings.TrimSpace(rule.ID) != "" {
			if previous, exists := seenIDs[rule.ID]; exists {
				problems = append(problems, fmt.Errorf("rules[%d].id %q duplicates rules[%d].id", index, rule.ID, previous))
			} else {
				seenIDs[rule.ID] = index
			}
		}

		convertedRule, err := rule.toFlowRule()
		if err != nil {
			problems = append(problems, fmt.Errorf("rules[%d]: %w", index, err))
			continue
		}
		converted[index] = convertedRule
	}

	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return converted, nil
}

func (rule RuleConfig) toFlowRule() (flow.Rule, error) {
	var problems []error
	if strings.TrimSpace(rule.ID) == "" {
		problems = append(problems, errors.New("id is required"))
	}

	action := flow.Action{State: flow.State(rule.Action.State), Channel: rule.Action.Channel}
	switch action.State {
	case flow.StateIgnore, flow.StateMonitor, flow.StatePlay:
	default:
		problems = append(problems, fmt.Errorf("action.state %q is invalid", rule.Action.State))
	}
	if action.Channel > 16 {
		problems = append(problems, errors.New("action.channel must be zero (inherit) or between 1 and 16"))
	}

	match := flow.Match{ExactFlowID: rule.Match.ExactFlowID, Protocol: rule.Match.Protocol}
	if match.ExactFlowID != "" {
		decoded, err := hex.DecodeString(match.ExactFlowID)
		if err != nil || len(match.ExactFlowID) != 24 || hex.EncodeToString(decoded) != match.ExactFlowID {
			problems = append(problems, errors.New("match.exact_flow_id must be 24 lowercase hexadecimal characters"))
		}
	}
	switch match.Protocol {
	case "", packet.ProtocolTCP, packet.ProtocolUDP, packet.ProtocolICMP, packet.ProtocolICMP6, packet.ProtocolOther:
	default:
		problems = append(problems, fmt.Errorf("match.protocol %q is invalid", rule.Match.Protocol))
	}

	if rule.Match.SourceCIDR != "" {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(rule.Match.SourceCIDR))
		if err != nil {
			problems = append(problems, fmt.Errorf("match.source_cidr: %w", err))
		} else {
			prefix = prefix.Masked()
			match.SourcePrefix = &prefix
		}
	}
	if rule.Match.DestinationCIDR != "" {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(rule.Match.DestinationCIDR))
		if err != nil {
			problems = append(problems, fmt.Errorf("match.destination_cidr: %w", err))
		} else {
			prefix = prefix.Masked()
			match.DestinationPrefix = &prefix
		}
	}

	if configured := rule.Match.SourcePorts; configured != nil {
		if configured.Minimum > configured.Maximum {
			problems = append(problems, errors.New("match.source_ports must be ordered"))
		} else {
			match.SourcePorts = &flow.PortRange{Minimum: configured.Minimum, Maximum: configured.Maximum}
		}
	}
	if configured := rule.Match.DestinationPorts; configured != nil {
		if configured.Minimum > configured.Maximum {
			problems = append(problems, errors.New("match.destination_ports must be ordered"))
		} else {
			match.DestinationPorts = &flow.PortRange{Minimum: configured.Minimum, Maximum: configured.Maximum}
		}
	}
	if configured := rule.Match.WireSize; configured != nil {
		if configured.Minimum < 0 || configured.Minimum > configured.Maximum {
			problems = append(problems, errors.New("match.wire_size must be non-negative and ordered"))
		} else {
			match.WireSize = &flow.SizeRange{Minimum: configured.Minimum, Maximum: configured.Maximum}
		}
	}

	seenFlags := make(map[TCPFlag]struct{}, len(rule.Match.RequiredTCPFlags))
	for flagIndex, configured := range rule.Match.RequiredTCPFlags {
		if _, exists := seenFlags[configured]; exists {
			problems = append(problems, fmt.Errorf("match.required_tcp_flags[%d] %q is duplicated", flagIndex, configured))
			continue
		}
		seenFlags[configured] = struct{}{}

		switch configured {
		case TCPFlagFIN:
			match.RequiredTCPFlags |= packet.TCPFlagFIN
		case TCPFlagSYN:
			match.RequiredTCPFlags |= packet.TCPFlagSYN
		case TCPFlagRST:
			match.RequiredTCPFlags |= packet.TCPFlagRST
		case TCPFlagPSH:
			match.RequiredTCPFlags |= packet.TCPFlagPSH
		case TCPFlagACK:
			match.RequiredTCPFlags |= packet.TCPFlagACK
		case TCPFlagURG:
			match.RequiredTCPFlags |= packet.TCPFlagURG
		default:
			problems = append(problems, fmt.Errorf("match.required_tcp_flags[%d] %q is invalid", flagIndex, configured))
		}
	}

	if err := errors.Join(problems...); err != nil {
		return flow.Rule{}, err
	}
	return flow.Rule{
		ID:      rule.ID,
		Name:    rule.Name,
		Enabled: rule.Enabled,
		Match:   match,
		Action:  action,
	}, nil
}
