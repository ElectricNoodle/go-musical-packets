package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
)

func TestManagementRuleHTTPUsesSharedRevisionAndPublishesImmediately(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	handler := newTestManagementHandler(t, backend)

	configResponse := serveTestManagementRequest(t, handler, http.MethodGet, "/api/v1/config", nil, nil)
	if configResponse.Code != http.StatusOK {
		t.Fatalf("GET config = %d, %q", configResponse.Code, configResponse.Body.String())
	}
	initialETag := configResponse.Header().Get("ETag")
	if initialETag == "" {
		t.Fatal("GET config ETag is empty")
	}

	rulesResponse := serveTestManagementRequest(t, handler, http.MethodGet, "/api/v1/rules", nil, nil)
	if rulesResponse.Code != http.StatusOK || rulesResponse.Header().Get("ETag") != initialETag {
		t.Fatalf("GET rules = %d ETag %q, want 200 and shared %q", rulesResponse.Code, rulesResponse.Header().Get("ETag"), initialETag)
	}
	var initial managementapi.RulesDocument
	if err := json.Unmarshal(rulesResponse.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode GET rules: %v", err)
	}
	if initial.Rules == nil || len(initial.Rules) != 0 || !initial.Writable {
		t.Fatalf("GET rules document = %#v, want writable empty collection", initial)
	}

	event := testPacket(41000, 443, time.Unix(100, 0))
	key, _ := flow.Canonicalize(event)
	rule := managementTestRule("pin/a?b", config.FlowPlay, 7)
	rule.Match.ExactFlowID = key.ID(configuration.Mapping.Seed)
	body, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("json.Marshal(rule) error = %v", err)
	}
	createResponse := serveTestManagementRequest(t, handler, http.MethodPost, "/api/v1/rules", body, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     initialETag,
	})
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("POST rule = %d, %q", createResponse.Code, createResponse.Body.String())
	}
	updatedETag := createResponse.Header().Get("ETag")
	if updatedETag == "" || updatedETag == initialETag {
		t.Fatalf("POST rule ETag = %q, want a new revision", updatedETag)
	}
	if location := createResponse.Header().Get("Location"); location != "/api/v1/rules/pin%2Fa%3Fb" {
		t.Fatalf("POST rule Location = %q", location)
	}
	var created managementapi.RulesDocument
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode POST rule: %v", err)
	}
	if created.Revision.String() != strings.Trim(updatedETag, `"`) {
		t.Fatalf("POST body revision = %q, ETag = %q", created.Revision, updatedETag)
	}
	assertManagementRuleIDs(t, created.Rules, rule.ID)
	assertSecretsAbsent(t, createResponse.Body.Bytes(), configuration)

	selection := mustSelect(t, controller, event)
	if selection.Tier != "pinned" || selection.RuleID != rule.ID || selection.State != flow.StatePlay || selection.Channel != 7 {
		t.Fatalf("selection after POST = %#v", selection)
	}
	durable, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	assertManagementRuleIDs(t, durable.Config.Rules, rule.ID)
	if durable.Config.Mapping.Seed != configuration.Mapping.Seed || durable.Config.Peer.URL != configuration.Peer.URL {
		t.Fatal("POST rule changed durable secrets")
	}

	staleDelete := serveTestManagementRequest(t, handler, http.MethodDelete, createResponse.Header().Get("Location"), nil, map[string]string{"If-Match": initialETag})
	if staleDelete.Code != http.StatusPreconditionFailed || staleDelete.Header().Get("ETag") != updatedETag {
		t.Fatalf("stale DELETE = %d ETag %q, want 412 and %q", staleDelete.Code, staleDelete.Header().Get("ETag"), updatedETag)
	}

	deleteResponse := serveTestManagementRequest(t, handler, http.MethodDelete, createResponse.Header().Get("Location"), nil, map[string]string{"If-Match": updatedETag})
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("DELETE rule = %d, %q", deleteResponse.Code, deleteResponse.Body.String())
	}
	var deleted managementapi.RulesDocument
	if err := json.Unmarshal(deleteResponse.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode DELETE rule: %v", err)
	}
	if deleted.Rules == nil || len(deleted.Rules) != 0 {
		t.Fatalf("DELETE rules = %#v, want nonnil empty collection", deleted.Rules)
	}
}
