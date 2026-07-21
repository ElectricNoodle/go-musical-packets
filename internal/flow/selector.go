package flow

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

// State controls whether matching traffic is ignored, observed, or musical.
type State string

const (
	StateIgnore  State = "ignore"
	StateMonitor State = "monitor"
	StatePlay    State = "play"
)

// PortRange is an inclusive transport-port range.
type PortRange struct {
	Minimum uint16
	Maximum uint16
}

// SizeRange is an inclusive packet wire-size range.
type SizeRange struct {
	Minimum int
	Maximum int
}

// Match contains optional predicates. Set predicates are combined with AND.
type Match struct {
	ExactFlowID       string
	SourcePrefix      *netip.Prefix
	DestinationPrefix *netip.Prefix
	Protocol          packet.Protocol
	SourcePorts       *PortRange
	DestinationPorts  *PortRange
	WireSize          *SizeRange
	RequiredTCPFlags  packet.TCPFlags
}

// Action is the routing result of a rule. Channel zero inherits the selector's
// default channel.
type Action struct {
	State   State
	Channel uint8
}

// Rule is an ordered traffic predicate and action.
type Rule struct {
	ID      string
	Name    string
	Enabled bool
	Match   Match
	Action  Action
}

// Overlay contains temporary, non-persisted UI mute and solo state.
type Overlay struct {
	Muted  map[string]struct{}
	Soloed map[string]struct{}
}

// Selection explains the effective routing decision.
type Selection struct {
	FlowID  string
	State   State
	Channel uint8
	RuleID  string
	Tier    string
}

// SelectorConfig establishes immutable rule precedence.
type SelectorConfig struct {
	Seed        string
	Default     Action
	SafetyRules []Rule
	PinnedRules []Rule
	UserRules   []Rule
}

// Selector evaluates immutable rule snapshots. UI changes construct a new
// selector and swap it atomically at the application boundary.
type Selector struct {
	seed          string
	defaultAction Action
	safetyRules   []Rule
	pinnedRules   []Rule
	userRules     []Rule
}

// NewSelector validates and copies a selector configuration.
func NewSelector(config SelectorConfig) (*Selector, error) {
	if config.Seed == "" {
		return nil, errors.New("selector seed is required")
	}
	if err := validateAction(config.Default); err != nil {
		return nil, fmt.Errorf("default action: %w", err)
	}
	if config.Default.Channel == 0 {
		return nil, errors.New("default action channel must be between 1 and 16")
	}

	seen := make(map[string]struct{})
	groups := []struct {
		name  string
		rules []Rule
	}{
		{name: "safety", rules: config.SafetyRules},
		{name: "pinned", rules: config.PinnedRules},
		{name: "user", rules: config.UserRules},
	}
	for _, group := range groups {
		for index, rule := range group.rules {
			if err := validateRule(rule); err != nil {
				return nil, fmt.Errorf("%s rule %d: %w", group.name, index, err)
			}
			if _, exists := seen[rule.ID]; exists {
				return nil, fmt.Errorf("duplicate rule ID %q", rule.ID)
			}
			seen[rule.ID] = struct{}{}
			if group.name == "safety" && rule.Action.State != StateIgnore {
				return nil, fmt.Errorf("safety rule %q must ignore traffic", rule.ID)
			}
			if group.name == "pinned" && rule.Match.ExactFlowID == "" {
				return nil, fmt.Errorf("pinned rule %q must match an exact flow ID", rule.ID)
			}
		}
	}

	return &Selector{
		seed:          config.Seed,
		defaultAction: config.Default,
		safetyRules:   cloneRules(config.SafetyRules),
		pinnedRules:   cloneRules(config.PinnedRules),
		userRules:     cloneRules(config.UserRules),
	}, nil
}

func cloneRules(rules []Rule) []Rule {
	if rules == nil {
		return nil
	}
	cloned := make([]Rule, len(rules))
	for index, rule := range rules {
		cloned[index] = rule
		cloned[index].Match = cloneMatch(rule.Match)
	}
	return cloned
}

func cloneMatch(match Match) Match {
	cloned := match
	cloned.SourcePrefix = cloneMatchValue(match.SourcePrefix)
	cloned.DestinationPrefix = cloneMatchValue(match.DestinationPrefix)
	cloned.SourcePorts = cloneMatchValue(match.SourcePorts)
	cloned.DestinationPorts = cloneMatchValue(match.DestinationPorts)
	cloned.WireSize = cloneMatchValue(match.WireSize)
	return cloned
}

func cloneMatchValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// Evaluate applies documented precedence to one packet.
func (s *Selector) Evaluate(event packet.Event, overlay Overlay) (Selection, error) {
	if err := event.Validate(); err != nil {
		return Selection{}, err
	}
	key, _ := Canonicalize(event)
	flowID := key.ID(s.seed)

	if rule, ok := firstMatch(s.safetyRules, event, flowID); ok {
		return s.selection(flowID, rule.Action, rule.ID, "safety"), nil
	}
	if _, muted := overlay.Muted[flowID]; muted {
		return s.selection(flowID, Action{State: StateIgnore}, "", "temporary_mute"), nil
	}
	if len(overlay.Soloed) > 0 {
		if _, soloed := overlay.Soloed[flowID]; !soloed {
			return s.selection(flowID, Action{State: StateMonitor}, "", "temporary_solo"), nil
		}
	}
	if rule, ok := firstMatch(s.pinnedRules, event, flowID); ok {
		return s.selection(flowID, rule.Action, rule.ID, "pinned"), nil
	}
	if rule, ok := firstMatch(s.userRules, event, flowID); ok {
		return s.selection(flowID, rule.Action, rule.ID, "user"), nil
	}
	return s.selection(flowID, s.defaultAction, "", "default"), nil
}

func (s *Selector) selection(flowID string, action Action, ruleID, tier string) Selection {
	channel := action.Channel
	if channel == 0 {
		channel = s.defaultAction.Channel
	}
	return Selection{FlowID: flowID, State: action.State, Channel: channel, RuleID: ruleID, Tier: tier}
}

func firstMatch(rules []Rule, event packet.Event, flowID string) (Rule, bool) {
	for _, rule := range rules {
		if rule.Enabled && rule.Match.matches(event, flowID) {
			return rule, true
		}
	}
	return Rule{}, false
}

func (m Match) matches(event packet.Event, flowID string) bool {
	if m.ExactFlowID != "" && m.ExactFlowID != flowID {
		return false
	}
	if m.SourcePrefix != nil && !m.SourcePrefix.Contains(event.Source.Addr) {
		return false
	}
	if m.DestinationPrefix != nil && !m.DestinationPrefix.Contains(event.Destination.Addr) {
		return false
	}
	if m.Protocol != "" && m.Protocol != event.Protocol {
		return false
	}
	if m.SourcePorts != nil && !m.SourcePorts.contains(event.Source.Port) {
		return false
	}
	if m.DestinationPorts != nil && !m.DestinationPorts.contains(event.Destination.Port) {
		return false
	}
	if m.WireSize != nil && (event.WireLength < m.WireSize.Minimum || event.WireLength > m.WireSize.Maximum) {
		return false
	}
	return m.RequiredTCPFlags == 0 || event.TCPFlags&m.RequiredTCPFlags == m.RequiredTCPFlags
}

func (r PortRange) contains(port uint16) bool {
	return port >= r.Minimum && port <= r.Maximum
}

func validateRule(rule Rule) error {
	if rule.ID == "" {
		return errors.New("rule ID is required")
	}
	if err := validateAction(rule.Action); err != nil {
		return err
	}
	if rule.Match.SourcePrefix != nil && !rule.Match.SourcePrefix.IsValid() {
		return errors.New("source prefix is invalid")
	}
	if rule.Match.DestinationPrefix != nil && !rule.Match.DestinationPrefix.IsValid() {
		return errors.New("destination prefix is invalid")
	}
	if rule.Match.SourcePorts != nil && rule.Match.SourcePorts.Minimum > rule.Match.SourcePorts.Maximum {
		return errors.New("source port range is reversed")
	}
	if rule.Match.DestinationPorts != nil && rule.Match.DestinationPorts.Minimum > rule.Match.DestinationPorts.Maximum {
		return errors.New("destination port range is reversed")
	}
	if rule.Match.WireSize != nil && (rule.Match.WireSize.Minimum < 0 || rule.Match.WireSize.Minimum > rule.Match.WireSize.Maximum) {
		return errors.New("wire-size range must be non-negative and ordered")
	}
	switch rule.Match.Protocol {
	case "", packet.ProtocolTCP, packet.ProtocolUDP, packet.ProtocolICMP, packet.ProtocolICMP6, packet.ProtocolOther:
	default:
		return errors.New("rule protocol is invalid")
	}
	return nil
}

func validateAction(action Action) error {
	switch action.State {
	case StateIgnore, StateMonitor, StatePlay:
	default:
		return fmt.Errorf("action state %q is invalid", action.State)
	}
	if action.Channel > 16 {
		return errors.New("action channel must be zero (inherit) or between 1 and 16")
	}
	return nil
}
