package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

func TestRulesCollectionGetAndHead(t *testing.T) {
	document := RulesDocument{
		Revision: testRevisionA,
		Writable: true,
		Rules: config.RulesConfig{
			testRule("first"),
			testRule("second"),
		},
	}
	calls := 0
	handler := mustHandler(t, &stubBackend{rulesFunc: func(ctx context.Context) (RulesDocument, error) {
		calls++
		if ctx == nil {
			t.Fatal("Rules context = nil")
		}
		return document, nil
	}})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := serve(handler, localRequestFor(method, rulesCollectionPath, ""))
			assertStatus(t, response, http.StatusOK)
			assertSecurityHeaders(t, response)
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := response.Header().Get("ETag"); got != `"`+testRevisionA+`"` {
				t.Fatalf("ETag = %q, want strong rules revision", got)
			}
			if response.Header().Get("Content-Length") == "" {
				t.Fatal("Content-Length is empty")
			}
			if method == http.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q, want empty", response.Body.String())
				}
				return
			}
			assertRulesDocument(t, response, document)
		})
	}
	if calls != 2 {
		t.Fatalf("Rules calls = %d, want 2", calls)
	}
}

func TestCreateRuleReturnsCollectionETagAndSafeLocation(t *testing.T) {
	tests := []struct {
		id       string
		location string
	}{
		{id: "plain", location: "/api/v1/rules/plain"},
		{id: "a/b", location: "/api/v1/rules/a%2Fb"},
		{id: "%2F", location: "/api/v1/rules/%252F"},
		{id: "question?mark", location: "/api/v1/rules/question%3Fmark"},
		{id: "hash#mark", location: "/api/v1/rules/hash%23mark"},
		{id: "a space", location: "/api/v1/rules/a%20space"},
		{id: "雪", location: "/api/v1/rules/%E9%9B%AA"},
		{id: ".", location: "/api/v1/rules/%2E"},
		{id: "..", location: "/api/v1/rules/%2E%2E"},
	}

	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			rule := testRule(test.id)
			created := RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{rule}}
			createCalls := 0
			deleteCalls := 0
			backend := &stubBackend{
				createRuleFunc: func(ctx context.Context, expected Revision, got config.RuleConfig) (RulesDocument, error) {
					createCalls++
					if ctx == nil {
						t.Fatal("CreateRule context = nil")
					}
					if expected != testRevisionA {
						t.Fatalf("CreateRule revision = %q, want %q", expected, testRevisionA)
					}
					if !reflect.DeepEqual(got, rule) {
						t.Fatalf("CreateRule rule = %#v, want %#v", got, rule)
					}
					return created, nil
				},
				deleteRuleFunc: func(_ context.Context, expected Revision, id string) (RulesDocument, error) {
					deleteCalls++
					if expected != testRevisionB {
						t.Fatalf("DeleteRule revision = %q, want %q", expected, testRevisionB)
					}
					if id != test.id {
						t.Fatalf("DeleteRule ID = %q, want %q", id, test.id)
					}
					return RulesDocument{Revision: testRevisionA, Writable: true, Rules: config.RulesConfig{}}, nil
				},
			}
			handler := mustHandler(t, backend)
			request := ruleJSONRequest(t, http.MethodPost, rulesCollectionPath, rule, testRevisionA)
			response := serve(handler, request)

			assertStatus(t, response, http.StatusCreated)
			assertSecurityHeaders(t, response)
			if got := response.Header().Get("ETag"); got != `"`+testRevisionB+`"` {
				t.Fatalf("ETag = %q, want new revision", got)
			}
			if got := response.Header().Get("Location"); got != test.location {
				t.Fatalf("Location = %q, want %q", got, test.location)
			}
			assertRulesDocument(t, response, created)

			follow := localRequestFor(http.MethodDelete, response.Header().Get("Location"), "")
			follow.Header.Set("If-Match", `"`+testRevisionB+`"`)
			assertStatus(t, serve(handler, follow), http.StatusOK)
			if createCalls != 1 || deleteCalls != 1 {
				t.Fatalf("backend calls = create %d delete %d, want 1 each", createCalls, deleteCalls)
			}
		})
	}
}

func TestReplaceDeleteAndReorderRules(t *testing.T) {
	replacement := testRule("a/b")
	wantDocument := RulesDocument{Revision: testRevisionB, Writable: false, Rules: config.RulesConfig{replacement, testRule("other")}}
	var replaceID string
	var replaced config.RuleConfig
	var deletedID string
	var order []string
	backend := &stubBackend{
		replaceRuleFunc: func(_ context.Context, expected Revision, id string, rule config.RuleConfig) (RulesDocument, error) {
			if expected != testRevisionA {
				t.Fatalf("ReplaceRule revision = %q, want %q", expected, testRevisionA)
			}
			replaceID = id
			replaced = rule
			return wantDocument, nil
		},
		deleteRuleFunc: func(_ context.Context, expected Revision, id string) (RulesDocument, error) {
			if expected != testRevisionA {
				t.Fatalf("DeleteRule revision = %q, want %q", expected, testRevisionA)
			}
			deletedID = id
			return wantDocument, nil
		},
		reorderRulesFunc: func(_ context.Context, expected Revision, got []string) (RulesDocument, error) {
			if expected != testRevisionA {
				t.Fatalf("ReorderRules revision = %q, want %q", expected, testRevisionA)
			}
			order = append([]string(nil), got...)
			return wantDocument, nil
		},
	}
	handler := mustHandler(t, backend)

	replaceRequest := ruleJSONRequest(t, http.MethodPut, rulesCollectionPath+"/a%2Fb", replacement, testRevisionA)
	replaceResponse := serve(handler, replaceRequest)
	assertStatus(t, replaceResponse, http.StatusOK)
	assertRulesResponse(t, replaceResponse, wantDocument)
	if replaceID != "a/b" || !reflect.DeepEqual(replaced, replacement) {
		t.Fatalf("ReplaceRule received ID %q rule %#v", replaceID, replaced)
	}

	deleteRequest := localRequestFor(http.MethodDelete, rulesCollectionPath+"/other", "")
	deleteRequest.Header.Set("If-Match", `"`+testRevisionA+`"`)
	deleteResponse := serve(handler, deleteRequest)
	assertStatus(t, deleteResponse, http.StatusOK)
	assertRulesResponse(t, deleteResponse, wantDocument)
	if deletedID != "other" {
		t.Fatalf("DeleteRule ID = %q, want other", deletedID)
	}

	patchRequest := localRequestFor(http.MethodPatch, rulesCollectionPath, `{"order":["other","a/b"]}`)
	patchRequest.Header.Set("Content-Type", "application/json; charset=UTF-8")
	patchRequest.Header.Set("If-Match", `"`+testRevisionA+`"`)
	patchResponse := serve(handler, patchRequest)
	assertStatus(t, patchResponse, http.StatusOK)
	assertRulesResponse(t, patchResponse, wantDocument)
	if !reflect.DeepEqual(order, []string{"other", "a/b"}) {
		t.Fatalf("ReorderRules order = %#v", order)
	}
}

func TestRuleItemDecodesExactlyOnceAndPreservesID(t *testing.T) {
	tests := []struct {
		path string
		id   string
	}{
		{path: "/api/v1/rules/plain", id: "plain"},
		{path: "/api/v1/rules/%2F", id: "/"},
		{path: "/api/v1/rules/%252F", id: "%2F"},
		{path: "/api/v1/rules/a%3Fb", id: "a?b"},
		{path: "/api/v1/rules/a%23b", id: "a#b"},
		{path: "/api/v1/rules/a%20b", id: "a b"},
		{path: "/api/v1/rules/%E9%9B%AA", id: "雪"},
		{path: "/api/v1/rules/.", id: "."},
		{path: "/api/v1/rules/..", id: ".."},
		{path: "/api/v1/rules/%2E", id: "."},
		{path: "/api/v1/rules/%2e%2e", id: ".."},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			var got string
			handler := mustHandler(t, &stubBackend{deleteRuleFunc: func(_ context.Context, _ Revision, id string) (RulesDocument, error) {
				got = id
				return RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{}}, nil
			}})
			request := localRequestFor(http.MethodDelete, test.path, "")
			request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			assertStatus(t, serve(handler, request), http.StatusOK)
			if got != test.id {
				t.Fatalf("decoded ID = %q, want %q", got, test.id)
			}
		})
	}
}

func TestRuleItemRejectsMalformedPaths(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		status int
		code   string
		fields []string
	}{
		{name: "empty item", path: "/api/v1/rules/", status: 404, code: "not_found"},
		{name: "raw slash", path: "/api/v1/rules/a/b", status: 400, code: "invalid_rule_id", fields: []string{"id"}},
		{name: "double raw slash", path: "/api/v1/rules//", status: 400, code: "invalid_rule_id", fields: []string{"id"}},
		{name: "invalid UTF-8", path: "/api/v1/rules/%FF", status: 400, code: "invalid_rule_id", fields: []string{"id"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{deleteRuleFunc: func(context.Context, Revision, string) (RulesDocument, error) {
				calls++
				return RulesDocument{}, nil
			}})
			request := localRequestFor(http.MethodDelete, test.path, "")
			request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			problem := assertProblem(t, serve(handler, request), test.status, test.code)
			if !reflect.DeepEqual(problem.Fields, test.fields) {
				t.Fatalf("fields = %#v, want %#v", problem.Fields, test.fields)
			}
			if calls != 0 {
				t.Fatalf("DeleteRule calls = %d, want 0", calls)
			}
		})
	}
}

func TestParseRuleItemIDRejectsMalformedEscape(t *testing.T) {
	if _, err := parseRuleItemID("/api/v1/rules/%zz"); err == nil {
		t.Fatal("parseRuleItemID(malformed escape) error = nil")
	}
}

func TestRuleRoutesRejectQueriesAndFragments(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		fragment string
		force    bool
	}{
		{name: "collection query", method: http.MethodGet, path: "/api/v1/rules?limit=1"},
		{name: "item query", method: http.MethodDelete, path: "/api/v1/rules/a?b"},
		{name: "empty query marker", method: http.MethodGet, path: "/api/v1/rules", force: true},
		{name: "fragment", method: http.MethodDelete, path: "/api/v1/rules/a", fragment: "b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			backend := &stubBackend{
				rulesFunc: func(context.Context) (RulesDocument, error) {
					calls++
					return RulesDocument{}, nil
				},
				deleteRuleFunc: func(context.Context, Revision, string) (RulesDocument, error) {
					calls++
					return RulesDocument{}, nil
				},
			}
			request := localRequestFor(test.method, test.path, "")
			request.URL.Fragment = test.fragment
			request.URL.ForceQuery = test.force
			request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			assertProblem(t, serve(mustHandler(t, backend), request), http.StatusBadRequest, "invalid_query")
			if calls != 0 {
				t.Fatalf("backend calls = %d, want 0", calls)
			}
		})
	}
}

func TestRuleRouteMethodMatrix(t *testing.T) {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
	tests := []struct {
		name    string
		path    string
		allow   string
		allowed map[string]int
	}{
		{
			name:  "collection",
			path:  rulesCollectionPath,
			allow: "GET, HEAD, POST, PATCH",
			allowed: map[string]int{
				http.MethodGet:   http.StatusOK,
				http.MethodHead:  http.StatusOK,
				http.MethodPost:  http.StatusCreated,
				http.MethodPatch: http.StatusOK,
			},
		},
		{
			name:  "item",
			path:  rulesCollectionPath + "/item",
			allow: "PUT, DELETE",
			allowed: map[string]int{
				http.MethodPut:    http.StatusOK,
				http.MethodDelete: http.StatusOK,
			},
		},
	}

	for _, test := range tests {
		for _, method := range methods {
			t.Run(test.name+"/"+method, func(t *testing.T) {
				body := ""
				if method == http.MethodPost || method == http.MethodPut {
					body = mustRuleJSON(t, testRule("item"))
				} else if method == http.MethodPatch {
					body = `{"order":[]}`
				}
				request := localRequestFor(method, test.path, body)
				if body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				request.Header.Set("If-Match", `"`+testRevisionA+`"`)
				response := serve(mustHandler(t, &stubBackend{}), request)
				want, allowed := test.allowed[method]
				if !allowed {
					want = http.StatusMethodNotAllowed
				}
				assertStatus(t, response, want)
				assertSecurityHeaders(t, response)
				wantAllow := ""
				if !allowed {
					wantAllow = test.allow
				}
				if got := response.Header().Get("Allow"); got != wantAllow {
					t.Fatalf("Allow = %q, want %q", got, wantAllow)
				}
				if method == http.MethodHead && response.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q, want empty", response.Body.String())
				}
			})
		}
	}
}

func TestRuleMutationsRequireValidIfMatchBeforeBody(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: rulesCollectionPath},
		{name: "reorder", method: http.MethodPatch, path: rulesCollectionPath},
		{name: "replace", method: http.MethodPut, path: rulesCollectionPath + "/item"},
		{name: "delete", method: http.MethodDelete, path: rulesCollectionPath + "/item"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(test.method, test.path, "not JSON")
			assertProblem(t, serve(mustHandler(t, &stubBackend{}), request), http.StatusPreconditionRequired, "precondition_required")

			request = localRequestFor(test.method, test.path, "not JSON")
			request.Header.Set("If-Match", `W/"`+testRevisionA+`"`)
			assertProblem(t, serve(mustHandler(t, &stubBackend{}), request), http.StatusBadRequest, "invalid_if_match")
		})
	}
}

func TestRuleJSONIsStrictAtEveryLevel(t *testing.T) {
	deep := strings.Repeat("[", maximumJSONDepth+1) + strings.Repeat("]", maximumJSONDepth+1)
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "malformed", body: `{"id":}`},
		{name: "top-level array", body: `[]`},
		{name: "unknown top-level", body: `{"id":"rule","unknown":true}`},
		{name: "case alias ID", body: `{"ID":"rule"}`},
		{name: "case alias match", body: `{"id":"rule","MATCH":{}}`},
		{name: "case alias match field", body: `{"id":"rule","match":{"Protocol":"tcp"}}`},
		{name: "unknown match field", body: `{"id":"rule","match":{"unknown":true}}`},
		{name: "case alias range", body: `{"id":"rule","match":{"source_ports":{"Minimum":1}}}`},
		{name: "unknown range field", body: `{"id":"rule","match":{"wire_size":{"unknown":1}}}`},
		{name: "case alias action", body: `{"id":"rule","action":{"State":"play"}}`},
		{name: "unknown action field", body: `{"id":"rule","action":{"unknown":true}}`},
		{name: "duplicate top-level", body: `{"id":"a","id":"b"}`},
		{name: "case variant duplicate", body: `{"id":"a","ID":"b"}`},
		{name: "duplicate match", body: `{"id":"a","match":{"protocol":"tcp","protocol":"udp"}}`},
		{name: "duplicate range", body: `{"id":"a","match":{"source_ports":{"minimum":1,"minimum":2}}}`},
		{name: "duplicate action", body: `{"id":"a","action":{"state":"play","state":"ignore"}}`},
		{name: "trailing value", body: `{"id":"a"} {}`},
		{name: "wrong field type", body: `{"id":1}`},
		{name: "excessive nesting", body: `{"id":"a","match":{"required_tcp_flags":` + deep + `}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{createRuleFunc: func(context.Context, Revision, config.RuleConfig) (RulesDocument, error) {
				calls++
				return RulesDocument{}, nil
			}})
			request := localRequestFor(http.MethodPost, rulesCollectionPath, test.body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
			if calls != 0 {
				t.Fatalf("CreateRule calls = %d, want 0", calls)
			}
		})
	}
}

func TestCreateRuleAcceptsMinimalCompatibleRepresentation(t *testing.T) {
	rule := config.RuleConfig{ID: "minimal", Action: config.RuleActionConfig{State: config.FlowMonitor}}
	var got config.RuleConfig
	handler := mustHandler(t, &stubBackend{createRuleFunc: func(_ context.Context, _ Revision, received config.RuleConfig) (RulesDocument, error) {
		got = received
		return RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{received}}, nil
	}})
	response := serve(handler, ruleJSONRequest(t, http.MethodPost, rulesCollectionPath, rule, testRevisionA))
	assertStatus(t, response, http.StatusCreated)
	if !reflect.DeepEqual(got, rule) {
		t.Fatalf("CreateRule rule = %#v, want %#v", got, rule)
	}
}

func TestReorderJSONIsStrict(t *testing.T) {
	tests := []string{
		`{"ORDER":[]}`,
		`{"order":[],"unknown":true}`,
		`{"order":[],"order":[]}`,
		`{"order":"rule"}`,
		`{"order":[]} {}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{reorderRulesFunc: func(context.Context, Revision, []string) (RulesDocument, error) {
				calls++
				return RulesDocument{}, nil
			}})
			request := localRequestFor(http.MethodPatch, rulesCollectionPath, body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
			if calls != 0 {
				t.Fatalf("ReorderRules calls = %d, want 0", calls)
			}
		})
	}
}

func TestRuleJSONRejectsInvalidUTF8(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{createRuleFunc: func(context.Context, Revision, config.RuleConfig) (RulesDocument, error) {
		calls++
		return RulesDocument{}, nil
	}})
	body := string(append([]byte(`{"id":"`), append([]byte{0xff}, []byte(`"}`)...)...))
	request := localRequestFor(http.MethodPost, rulesCollectionPath, body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"`+testRevisionA+`"`)
	assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
	if calls != 0 {
		t.Fatalf("CreateRule calls = %d, want 0", calls)
	}
}

func TestRuleJSONMediaTypeAndEncoding(t *testing.T) {
	rule := testRule("rule")
	body := mustRuleJSON(t, rule)
	tests := []struct {
		name        string
		contentType []string
		encoding    []string
		status      int
		code        string
	}{
		{name: "missing media type", status: 415, code: "unsupported_media_type"},
		{name: "YAML", contentType: []string{"application/yaml"}, status: 415, code: "unsupported_media_type"},
		{name: "bad charset", contentType: []string{"application/json; charset=latin1"}, status: 415, code: "unsupported_media_type"},
		{name: "unknown parameter", contentType: []string{"application/json; profile=rule"}, status: 415, code: "unsupported_media_type"},
		{name: "duplicate media type", contentType: []string{"application/json", "application/json"}, status: 415, code: "unsupported_media_type"},
		{name: "gzip", contentType: []string{"application/json"}, encoding: []string{"gzip"}, status: 415, code: "unsupported_content_encoding"},
		{name: "encoding list", contentType: []string{"application/json"}, encoding: []string{"identity, gzip"}, status: 415, code: "unsupported_content_encoding"},
		{name: "duplicate encoding", contentType: []string{"application/json"}, encoding: []string{"identity", "identity"}, status: 415, code: "unsupported_content_encoding"},
		{name: "exact", contentType: []string{"application/json"}, status: 201},
		{name: "UTF-8", contentType: []string{"application/json; charset=UTF-8"}, encoding: []string{" Identity "}, status: 201},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, rulesCollectionPath, body)
			request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			for _, value := range test.contentType {
				request.Header.Add("Content-Type", value)
			}
			for _, value := range test.encoding {
				request.Header.Add("Content-Encoding", value)
			}
			response := serve(mustHandler(t, &stubBackend{}), request)
			if test.code == "" {
				assertStatus(t, response, test.status)
			} else {
				assertProblem(t, response, test.status, test.code)
			}
		})
	}
}

func TestRuleJSONBodyLimitAndReadFailure(t *testing.T) {
	base := mustRuleJSON(t, testRule("rule"))
	exact := base + strings.Repeat(" ", config.MaximumBytes-len(base))
	handler := mustHandler(t, &stubBackend{})

	request := localRequestFor(http.MethodPost, rulesCollectionPath, exact)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"`+testRevisionA+`"`)
	assertStatus(t, serve(handler, request), http.StatusCreated)

	request = localRequestFor(http.MethodPost, rulesCollectionPath, exact+" ")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"`+testRevisionA+`"`)
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	assertProblem(t, serve(handler, request), http.StatusRequestEntityTooLarge, "body_too_large")

	request = localRequestFor(http.MethodPost, rulesCollectionPath, "")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"`+testRevisionA+`"`)
	request.Body = errorReader{}
	assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
}

func TestRuleBackendErrorsAndInvalidDocuments(t *testing.T) {
	backendError := &BackendError{Kind: ErrorUnavailable, Err: errors.New("private failure")}
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		backend *stubBackend
	}{
		{name: "list", method: http.MethodGet, path: rulesCollectionPath, backend: &stubBackend{rulesFunc: func(context.Context) (RulesDocument, error) { return RulesDocument{}, backendError }}},
		{name: "create", method: http.MethodPost, path: rulesCollectionPath, body: mustRuleJSON(t, testRule("rule")), backend: &stubBackend{createRuleFunc: func(context.Context, Revision, config.RuleConfig) (RulesDocument, error) {
			return RulesDocument{}, backendError
		}}},
		{name: "replace", method: http.MethodPut, path: rulesCollectionPath + "/rule", body: mustRuleJSON(t, testRule("rule")), backend: &stubBackend{replaceRuleFunc: func(context.Context, Revision, string, config.RuleConfig) (RulesDocument, error) {
			return RulesDocument{}, backendError
		}}},
		{name: "delete", method: http.MethodDelete, path: rulesCollectionPath + "/rule", backend: &stubBackend{deleteRuleFunc: func(context.Context, Revision, string) (RulesDocument, error) { return RulesDocument{}, backendError }}},
		{name: "reorder", method: http.MethodPatch, path: rulesCollectionPath, body: `{"order":[]}`, backend: &stubBackend{reorderRulesFunc: func(context.Context, Revision, []string) (RulesDocument, error) { return RulesDocument{}, backendError }}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(test.method, test.path, test.body)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			if test.method != http.MethodGet {
				request.Header.Set("If-Match", `"`+testRevisionA+`"`)
			}
			response := serve(mustHandler(t, test.backend), request)
			assertProblem(t, response, http.StatusServiceUnavailable, "unavailable")
			if strings.Contains(response.Body.String(), "private failure") {
				t.Fatalf("response leaked backend error: %s", response.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name     string
		revision Revision
	}{
		{name: "empty", revision: ""},
		{name: "malformed", revision: "not-a-revision"},
	} {
		t.Run("invalid document "+test.name, func(t *testing.T) {
			handler := mustHandler(t, &stubBackend{rulesFunc: func(context.Context) (RulesDocument, error) {
				return RulesDocument{Revision: test.revision, Rules: config.RulesConfig{}}, nil
			}})
			response := serve(handler, localRequestFor(http.MethodGet, rulesCollectionPath, ""))
			assertProblem(t, response, http.StatusInternalServerError, "internal_error")
			if response.Header().Get("ETag") != "" {
				t.Fatalf("ETag = %q, want empty", response.Header().Get("ETag"))
			}
		})
	}

	handler := mustHandler(t, &stubBackend{createRuleFunc: func(context.Context, Revision, config.RuleConfig) (RulesDocument, error) {
		return RulesDocument{Revision: "bad"}, nil
	}})
	response := serve(handler, ruleJSONRequest(t, http.MethodPost, rulesCollectionPath, testRule("rule"), testRevisionA))
	assertProblem(t, response, http.StatusInternalServerError, "internal_error")
	if response.Header().Get("Location") != "" || response.Header().Get("ETag") != "" {
		t.Fatalf("invalid response headers: Location %q ETag %q", response.Header().Get("Location"), response.Header().Get("ETag"))
	}
}

func testRule(id string) config.RuleConfig {
	return config.RuleConfig{
		ID:      id,
		Name:    "Rule " + id,
		Enabled: true,
		Match: config.RuleMatchConfig{
			Protocol:         "tcp",
			SourcePorts:      &config.PortRangeConfig{Minimum: 1000, Maximum: 2000},
			DestinationPorts: &config.PortRangeConfig{Minimum: 80, Maximum: 443},
			WireSize:         &config.SizeRangeConfig{Minimum: 64, Maximum: 1500},
			RequiredTCPFlags: []config.TCPFlag{config.TCPFlagSYN},
		},
		Action: config.RuleActionConfig{State: config.FlowPlay, Channel: 4},
	}
}

func mustRuleJSON(t *testing.T, rule config.RuleConfig) string {
	t.Helper()
	contents, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("json.Marshal(rule) error = %v", err)
	}
	return string(contents)
}

func ruleJSONRequest(t *testing.T, method, path string, rule config.RuleConfig, revision Revision) *http.Request {
	t.Helper()
	request := localRequestFor(method, path, mustRuleJSON(t, rule))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"`+revision.String()+`"`)
	return request
}

func assertRulesResponse(t *testing.T, response *httptest.ResponseRecorder, want RulesDocument) {
	t.Helper()
	if got := response.Header().Get("ETag"); got != `"`+want.Revision.String()+`"` {
		t.Fatalf("ETag = %q, want revision %q", got, want.Revision)
	}
	assertRulesDocument(t, response, want)
}

func assertRulesDocument(t *testing.T, response *httptest.ResponseRecorder, want RulesDocument) {
	t.Helper()
	var got RulesDocument
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode rules document: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules document = %#v, want %#v", got, want)
	}
}
