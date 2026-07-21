package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
)

// Rules returns the ordered persistent rule set without exposing any other
// configuration field or durable repository revision.
func (backend *managementBackend) Rules(ctx context.Context) (managementapi.RulesDocument, error) {
	if ctx == nil {
		return managementapi.RulesDocument{}, managementRuleInvalid(
			"invalid_rule",
			errors.New("management rule context is required"),
		)
	}
	if err := ctx.Err(); err != nil {
		return managementapi.RulesDocument{}, err
	}
	return backend.managementRulesDocument(backend.controller.Current()), nil
}

// CreateRule appends a rule to the ordered persistent rule set.
func (backend *managementBackend) CreateRule(ctx context.Context, expected managementapi.Revision, rule config.RuleConfig) (managementapi.RulesDocument, error) {
	rule = cloneManagementRule(rule)
	return backend.mutateRules(ctx, expected, func(candidate *config.Config) error {
		for _, existing := range candidate.Rules {
			if existing.ID == rule.ID {
				return managementRuleConflict(
					"rule_exists",
					fmt.Errorf("rule %q already exists", rule.ID),
				)
			}
		}
		candidate.Rules = append(candidate.Rules, rule)
		return validateManagementRules(candidate)
	})
}

// ReplaceRules atomically replaces the complete ordered persistent rule set.
func (backend *managementBackend) ReplaceRules(ctx context.Context, expected managementapi.Revision, rules config.RulesConfig) (managementapi.RulesDocument, error) {
	rulesWasNil := rules == nil
	rules = (config.Config{Rules: rules}).Clone().Rules
	return backend.mutateRules(ctx, expected, func(candidate *config.Config) error {
		if rulesWasNil {
			return managementRuleInvalid("invalid_rule", errors.New("rules must be an array"))
		}
		candidate.Rules = rules
		return validateManagementRules(candidate)
	})
}

// ReplaceRule replaces a rule in place, preserving the order of all rules.
func (backend *managementBackend) ReplaceRule(ctx context.Context, expected managementapi.Revision, id string, rule config.RuleConfig) (managementapi.RulesDocument, error) {
	rule = cloneManagementRule(rule)
	return backend.mutateRules(ctx, expected, func(candidate *config.Config) error {
		if rule.ID != id {
			return managementRuleConflict(
				"rule_id_mismatch",
				fmt.Errorf("rule body id %q does not match path id %q", rule.ID, id),
			)
		}
		for index := range candidate.Rules {
			if candidate.Rules[index].ID != id {
				continue
			}
			candidate.Rules[index] = rule
			return validateManagementRules(candidate)
		}
		return managementRuleNotFound(id)
	})
}

// DeleteRule removes one rule while retaining the relative order of the
// remaining rules.
func (backend *managementBackend) DeleteRule(ctx context.Context, expected managementapi.Revision, id string) (managementapi.RulesDocument, error) {
	return backend.mutateRules(ctx, expected, func(candidate *config.Config) error {
		for index := range candidate.Rules {
			if candidate.Rules[index].ID != id {
				continue
			}
			copy(candidate.Rules[index:], candidate.Rules[index+1:])
			candidate.Rules[len(candidate.Rules)-1] = config.RuleConfig{}
			candidate.Rules = candidate.Rules[:len(candidate.Rules)-1]
			return validateManagementRules(candidate)
		}
		return managementRuleNotFound(id)
	})
}

// ReorderRules applies a complete permutation of the current rule IDs.
func (backend *managementBackend) ReorderRules(ctx context.Context, expected managementapi.Revision, order []string) (managementapi.RulesDocument, error) {
	orderWasNil := order == nil
	order = append([]string(nil), order...)
	return backend.mutateRules(ctx, expected, func(candidate *config.Config) error {
		if orderWasNil {
			return managementRuleOrderInvalid(errors.New("rule order must be an array"))
		}
		if len(order) != len(candidate.Rules) {
			return managementRuleOrderInvalid(fmt.Errorf(
				"rule order contains %d IDs, want %d",
				len(order),
				len(candidate.Rules),
			))
		}
		if len(order) == 0 {
			return validateManagementRules(candidate)
		}

		byID := make(map[string]config.RuleConfig, len(candidate.Rules))
		for _, rule := range candidate.Rules {
			byID[rule.ID] = rule
		}
		reordered := make(config.RulesConfig, len(order))
		seen := make(map[string]struct{}, len(order))
		for index, id := range order {
			if _, duplicate := seen[id]; duplicate {
				return managementRuleOrderInvalid(fmt.Errorf("rule order ID %q is duplicated", id))
			}
			seen[id] = struct{}{}
			rule, found := byID[id]
			if !found {
				return managementRuleOrderInvalid(fmt.Errorf("rule order ID %q does not exist", id))
			}
			reordered[index] = rule
		}
		candidate.Rules = reordered
		return validateManagementRules(candidate)
	})
}

type managementRuleMutation func(*config.Config) error

func (backend *managementBackend) mutateRules(ctx context.Context, expected managementapi.Revision, mutation managementRuleMutation) (managementapi.RulesDocument, error) {
	if ctx == nil {
		return managementapi.RulesDocument{}, managementRuleInvalid(
			"invalid_rule",
			errors.New("management rule context is required"),
		)
	}
	lifecycle := backend.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	if !backend.ready.Load() || lifecycle.Err() != nil {
		return managementapi.RulesDocument{}, managementRuleUnavailable(
			errors.New("runtime is starting or stopping"),
		)
	}

	current := backend.controller.Current()
	rawExpected := backend.revisions.resolve(expected, current.Revision)
	mutationContext, cancelMutation := context.WithCancel(lifecycle)
	stopRequestCancellation := context.AfterFunc(ctx, cancelMutation)
	defer func() {
		stopRequestCancellation()
		cancelMutation()
	}()
	if ctx.Err() != nil {
		cancelMutation()
	}
	if err := mutationContext.Err(); err != nil {
		return managementapi.RulesDocument{}, backend.managementUpdateError(err)
	}

	updated, err := backend.controller.MutateContext(mutationContext, rawExpected, func(candidate *config.Config) error {
		return mutation(candidate)
	})
	if err != nil {
		var backendError *managementapi.BackendError
		if errors.As(err, &backendError) {
			return managementapi.RulesDocument{}, backendError
		}
		return managementapi.RulesDocument{}, backend.managementUpdateError(err)
	}
	return backend.managementRulesDocument(updated), nil
}

func (backend *managementBackend) managementRulesDocument(document Document) managementapi.RulesDocument {
	rules := document.Config.Clone().Rules
	if rules == nil {
		rules = make(config.RulesConfig, 0)
	}
	return managementapi.RulesDocument{
		Rules:    rules,
		Revision: backend.revisions.issue(document.Revision),
		Writable: document.Writable,
	}
}

func cloneManagementRule(rule config.RuleConfig) config.RuleConfig {
	clone := config.Config{Rules: config.RulesConfig{rule}}.Clone()
	return clone.Rules[0]
}

func validateManagementRules(candidate *config.Config) error {
	if _, err := candidate.Rules.ToFlowRules(); err != nil {
		return managementRuleInvalid("invalid_rule", err)
	}
	if err := validateManagementSize(*candidate); err != nil {
		return managementRuleInvalid("invalid_rule", err)
	}
	return nil
}

func managementRuleNotFound(id string) error {
	err := fmt.Errorf("rule %q was not found", id)
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorNotFound,
		Code:   "rule_not_found",
		Detail: "rule was not found",
		Fields: []string{"id"},
		Err:    err,
	}
}

func managementRuleConflict(code string, err error) error {
	detail := "rule ID already exists"
	if code == "rule_id_mismatch" {
		detail = "rule body ID must match path ID"
	}
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorConflict,
		Code:   code,
		Detail: detail,
		Fields: []string{"id"},
		Err:    err,
	}
}

func managementRuleInvalid(code string, err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorInvalid,
		Code:   code,
		Detail: "rule is invalid",
		Fields: []string{"rule"},
		Err:    err,
	}
}

func managementRuleOrderInvalid(err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorInvalid,
		Code:   "invalid_rule_order",
		Detail: "rule order must be a complete unique permutation",
		Fields: []string{"order"},
		Err:    err,
	}
}

func managementRuleUnavailable(err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorUnavailable,
		Code:   "runtime_unavailable",
		Detail: "runtime is starting or stopping",
		Err:    err,
	}
}
