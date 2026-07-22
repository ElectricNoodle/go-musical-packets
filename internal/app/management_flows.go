package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

const maximumManagementFlowPageLimit = 5000

// Flows returns one bounded, newest-first registry snapshot together with the
// complete temporary overlay generation used to annotate it.
func (backend *managementBackend) Flows(ctx context.Context, request managementapi.FlowPageRequest) (managementapi.FlowPage, error) {
	if ctx == nil {
		return managementapi.FlowPage{}, managementFlowInvalid("invalid_flow_page", errors.New("management flow context is required"))
	}
	if request.Limit < 1 || request.Limit > maximumManagementFlowPageLimit {
		return managementapi.FlowPage{}, managementFlowInvalid(
			"invalid_flow_page",
			fmt.Errorf("flow page limit must be between 1 and %d", maximumManagementFlowPageLimit),
		)
	}
	if err := backend.flowRuntimeAvailable(ctx, false); err != nil {
		return managementapi.FlowPage{}, err
	}

	policy := backend.controller.store.current.Load()
	if policy == nil || policy.selector == nil {
		return managementapi.FlowPage{}, managementFlowUnavailable(errors.New("runtime policy is unavailable"))
	}
	snapshots, total := backend.registry.RecentSnapshots(request.Limit)
	overlay := policy.overlay
	flows := make([]managementapi.FlowSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		_, muted := overlay.Muted[snapshot.ID]
		_, soloed := overlay.Soloed[snapshot.ID]
		converted, err := managementFlowSnapshot(snapshot, muted, soloed, policy.selector, overlay, policy.configuration.Rules)
		if err != nil {
			return managementapi.FlowPage{}, fmt.Errorf("annotate flow %q: %w", snapshot.ID, err)
		}
		flows = append(flows, converted)
	}
	return managementapi.FlowPage{
		Flows:     flows,
		Overlay:   managementFlowOverlay(overlay),
		Total:     total,
		Limit:     request.Limit,
		Truncated: total > len(flows),
	}, nil
}

// SetMutedFlows replaces the complete process-local mute set.
func (backend *managementBackend) SetMutedFlows(ctx context.Context, flowIDs []string) (managementapi.FlowOverlay, error) {
	return backend.setFlowOverlay(ctx, flowIDs, true)
}

// SetSoloedFlows replaces the complete process-local solo set.
func (backend *managementBackend) SetSoloedFlows(ctx context.Context, flowIDs []string) (managementapi.FlowOverlay, error) {
	return backend.setFlowOverlay(ctx, flowIDs, false)
}

func (backend *managementBackend) setFlowOverlay(ctx context.Context, flowIDs []string, mute bool) (managementapi.FlowOverlay, error) {
	if ctx == nil {
		return managementapi.FlowOverlay{}, managementFlowInvalid("invalid_flow_set", errors.New("management flow context is required"))
	}
	if err := backend.flowRuntimeAvailable(ctx, true); err != nil {
		return managementapi.FlowOverlay{}, err
	}
	if flowIDs == nil {
		return managementapi.FlowOverlay{}, managementFlowInvalid("invalid_flow_set", errors.New("flow_ids must be an array"))
	}
	values := make(map[string]struct{}, len(flowIDs))
	for index, flowID := range flowIDs {
		if _, duplicate := values[flowID]; duplicate {
			return managementapi.FlowOverlay{}, managementFlowInvalid(
				"invalid_flow_set",
				fmt.Errorf("flow_ids[%d] duplicates flow ID %q", index, flowID),
			)
		}
		values[flowID] = struct{}{}
	}
	mutationContext, cancelMutation := backend.flowMutationContext(ctx)
	defer cancelMutation()

	var (
		overlay flow.Overlay
		err     error
	)
	if mute {
		overlay, err = backend.controller.ReplaceMuteContext(mutationContext, values)
	} else {
		overlay, err = backend.controller.ReplaceSoloContext(mutationContext, values)
	}
	if err != nil {
		var stateError *policyStateError
		if errors.As(err, &stateError) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return managementapi.FlowOverlay{}, managementFlowUnavailable(err)
		}
		return managementapi.FlowOverlay{}, managementFlowInvalid("invalid_flow_set", err)
	}
	return managementFlowOverlay(overlay), nil
}

func (backend *managementBackend) flowMutationContext(request context.Context) (context.Context, context.CancelFunc) {
	lifecycle := backend.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	ctx, cancel := context.WithCancel(lifecycle)
	stopRequestCancellation := context.AfterFunc(request, cancel)
	if request.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stopRequestCancellation()
		cancel()
	}
}

func (backend *managementBackend) flowRuntimeAvailable(ctx context.Context, requireReadyPolicy bool) error {
	if err := ctx.Err(); err != nil {
		return managementFlowUnavailable(err)
	}
	lifecycle := backend.lifecycle
	if lifecycle != nil {
		if err := lifecycle.Err(); err != nil {
			return managementFlowUnavailable(err)
		}
	}
	if !backend.ready.Load() {
		return managementFlowUnavailable(errors.New("runtime is starting or stopping"))
	}
	if requireReadyPolicy {
		state := backend.controller.store.current.Load().state
		if state != ControllerStateReady && state != ControllerStateRestartPending {
			return managementFlowUnavailable(fmt.Errorf("runtime configuration state is %s", state))
		}
	}
	return nil
}

func managementFlowSnapshot(
	snapshot flow.Snapshot,
	muted, soloed bool,
	selector *flow.Selector,
	overlay flow.Overlay,
	rules config.RulesConfig,
) (managementapi.FlowSnapshot, error) {
	selection, err := selector.Evaluate(snapshot.LastEvent, overlay)
	if err != nil {
		return managementapi.FlowSnapshot{}, fmt.Errorf("evaluate current policy: %w", err)
	}
	identity, err := music.IdentityForFlowID(snapshot.ID)
	if err != nil {
		return managementapi.FlowSnapshot{}, fmt.Errorf("derive musical identity: %w", err)
	}
	if selection.Mode != "" {
		identity.Mode, err = music.ParseMode(selection.Mode)
		if err != nil {
			return managementapi.FlowSnapshot{}, fmt.Errorf("derive fixed musical identity: %w", err)
		}
		identity.Root = selection.Root
	}
	ruleName, reason, predicates := managementFlowExplanation(selection, rules)
	return managementapi.FlowSnapshot{
		ID:       snapshot.ID,
		Protocol: string(snapshot.Key.Protocol),
		EndpointA: managementapi.FlowEndpoint{
			Address: snapshot.Key.A.Addr.String(),
			Port:    snapshot.Key.A.Port,
		},
		EndpointB: managementapi.FlowEndpoint{
			Address: snapshot.Key.B.Addr.String(),
			Port:    snapshot.Key.B.Port,
		},
		LatestSource: managementapi.FlowEndpoint{
			Address: snapshot.LastEvent.Source.Addr.String(),
			Port:    snapshot.LastEvent.Source.Port,
		},
		LatestDestination: managementapi.FlowEndpoint{
			Address: snapshot.LastEvent.Destination.Addr.String(),
			Port:    snapshot.LastEvent.Destination.Port,
		},
		FirstSeen:         snapshot.FirstSeen,
		LastSeen:          snapshot.LastSeen,
		Packets:           snapshot.Packets,
		Bytes:             snapshot.Bytes,
		PacketsAToB:       snapshot.PacketsAToB,
		PacketsBToA:       snapshot.PacketsBToA,
		Muted:             muted,
		Soloed:            soloed,
		State:             string(selection.State),
		Channel:           selection.Channel,
		RuleID:            selection.RuleID,
		RuleTier:          selection.Tier,
		RuleName:          ruleName,
		DecisionReason:    reason,
		MatchedPredicates: predicates,
		Mode:              identity.Mode.String(),
		Root:              identity.Root,
		FixedIdentity:     selection.Mode != "",
	}, nil
}

func managementFlowExplanation(selection flow.Selection, rules config.RulesConfig) (string, string, []string) {
	predicates := make([]string, 0)
	switch selection.Tier {
	case "temporary_mute":
		return "", "the flow is in the temporary mute set", predicates
	case "temporary_solo":
		return "", "another flow is soloed, so this flow is monitored", predicates
	case "default":
		return "", "no higher-precedence rule matched the latest packet", predicates
	case "safety":
		return "", fmt.Sprintf("safety rule %s matched the latest packet", selection.RuleID), predicates
	}
	for _, rule := range rules {
		if rule.ID != selection.RuleID {
			continue
		}
		match := rule.Match
		if match.ExactFlowID != "" {
			predicates = append(predicates, "exact flow "+match.ExactFlowID)
		}
		if match.SourceCIDR != "" {
			predicates = append(predicates, "source in "+match.SourceCIDR)
		}
		if match.DestinationCIDR != "" {
			predicates = append(predicates, "destination in "+match.DestinationCIDR)
		}
		if match.Protocol != "" {
			predicates = append(predicates, "protocol "+string(match.Protocol))
		}
		if match.SourcePorts != nil {
			predicates = append(predicates, "source ports "+managementPortRange(*match.SourcePorts))
		}
		if match.DestinationPorts != nil {
			predicates = append(predicates, "destination ports "+managementPortRange(*match.DestinationPorts))
		}
		if match.WireSize != nil {
			predicates = append(predicates, fmt.Sprintf("wire size %d-%d bytes", match.WireSize.Minimum, match.WireSize.Maximum))
		}
		if len(match.RequiredTCPFlags) > 0 {
			flags := make([]string, len(match.RequiredTCPFlags))
			for index, flag := range match.RequiredTCPFlags {
				flags[index] = string(flag)
			}
			predicates = append(predicates, "TCP flags "+strings.Join(flags, "+"))
		}
		return rule.Name, fmt.Sprintf("%s rule %s matched every configured predicate", selection.Tier, selection.RuleID), predicates
	}
	return "", fmt.Sprintf("%s rule %s matched the latest packet", selection.Tier, selection.RuleID), predicates
}

func managementPortRange(value config.PortRangeConfig) string {
	if value.Minimum == value.Maximum {
		return fmt.Sprintf("%d", value.Minimum)
	}
	return fmt.Sprintf("%d-%d", value.Minimum, value.Maximum)
}

func managementFlowOverlay(overlay flow.Overlay) managementapi.FlowOverlay {
	muted := make([]string, 0, len(overlay.Muted))
	for flowID := range overlay.Muted {
		muted = append(muted, flowID)
	}
	soloed := make([]string, 0, len(overlay.Soloed))
	for flowID := range overlay.Soloed {
		soloed = append(soloed, flowID)
	}
	sort.Strings(muted)
	sort.Strings(soloed)
	return managementapi.FlowOverlay{Muted: muted, Soloed: soloed}
}

func managementFlowInvalid(code string, err error) error {
	field := "flow_ids"
	detail := "flow_ids must contain unique 24-character lowercase hexadecimal identifiers within the configured limit"
	if code == "invalid_flow_page" {
		field = "limit"
		detail = "flow page limit is invalid"
	}
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorInvalid,
		Code:   code,
		Detail: detail,
		Fields: []string{field},
		Err:    err,
	}
}

func managementFlowUnavailable(err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorUnavailable,
		Code:   "runtime_unavailable",
		Detail: "runtime is starting or stopping",
		Err:    err,
	}
}
