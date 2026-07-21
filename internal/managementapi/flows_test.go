package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

const (
	testFlowA = "0123456789abcdef01234567"
	testFlowB = "89abcdef0123456701234567"
)

func TestFlowsGetAndHead(t *testing.T) {
	want := FlowPage{
		Flows: []FlowSnapshot{{
			ID:                testFlowA,
			Protocol:          "tcp",
			EndpointA:         FlowEndpoint{Address: "192.0.2.1", Port: 43210},
			EndpointB:         FlowEndpoint{Address: "198.51.100.2", Port: 443},
			LatestSource:      FlowEndpoint{Address: "192.0.2.1", Port: 43210},
			LatestDestination: FlowEndpoint{Address: "198.51.100.2", Port: 443},
			FirstSeen:         time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC),
			LastSeen:          time.Date(2026, 7, 21, 10, 0, 1, 0, time.UTC),
			Packets:           3,
			Bytes:             750,
			PacketsAToB:       2,
			PacketsBToA:       1,
			Muted:             true,
			State:             "ignore",
			Channel:           7,
			RuleID:            "temporary-rule",
			RuleTier:          "temporary_mute",
			DecisionReason:    "the flow is in the temporary mute set",
			MatchedPredicates: []string{},
			Mode:              "dorian",
			Root:              2,
		}},
		Overlay:   FlowOverlay{Muted: []string{testFlowA}, Soloed: []string{testFlowB}},
		Total:     2,
		Limit:     defaultFlowPageLimit,
		Truncated: true,
	}
	requests := make([]FlowPageRequest, 0, 2)
	handler := mustHandler(t, &stubBackend{flowsFunc: func(ctx context.Context, request FlowPageRequest) (FlowPage, error) {
		if ctx == nil {
			t.Fatal("Flows context = nil")
		}
		requests = append(requests, request)
		page := want
		page.Limit = request.Limit
		return page, nil
	}})

	getResponse := serve(handler, localRequestFor(http.MethodGet, "/api/v1/flows", ""))
	assertStatus(t, getResponse, http.StatusOK)
	assertSecurityHeaders(t, getResponse)
	if got := getResponse.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("GET Content-Type = %q, want application/json", got)
	}
	if got := getResponse.Header().Get("ETag"); got != "" {
		t.Fatalf("GET ETag = %q, want empty for a live representation", got)
	}
	var got FlowPage
	if err := json.Unmarshal(getResponse.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode flow page: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flow page = %#v, want %#v", got, want)
	}

	headResponse := serve(handler, localRequestFor(http.MethodHead, "/api/v1/flows?limit=7", ""))
	assertStatus(t, headResponse, http.StatusOK)
	assertSecurityHeaders(t, headResponse)
	if headResponse.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", headResponse.Body.String())
	}
	if headResponse.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD Content-Length is empty")
	}
	if got := headResponse.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("HEAD Content-Type = %q, want application/json", got)
	}
	if !reflect.DeepEqual(requests, []FlowPageRequest{{Limit: defaultFlowPageLimit}, {Limit: 7}}) {
		t.Fatalf("Flows requests = %#v", requests)
	}
}

func TestFlowPageLimitValidation(t *testing.T) {
	valid := []struct {
		query string
		limit int
	}{
		{query: "", limit: defaultFlowPageLimit},
		{query: "limit=1", limit: 1},
		{query: "limit=0500", limit: 500},
		{query: "limit=5000", limit: maximumFlowPageLimit},
	}
	for _, test := range valid {
		t.Run("valid/"+test.query, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{flowsFunc: func(_ context.Context, request FlowPageRequest) (FlowPage, error) {
				calls++
				if request.Limit != test.limit {
					t.Fatalf("limit = %d, want %d", request.Limit, test.limit)
				}
				return FlowPage{Flows: []FlowSnapshot{}, Overlay: FlowOverlay{Muted: []string{}, Soloed: []string{}}, Limit: request.Limit}, nil
			}})
			response := serve(handler, localRequestFor(http.MethodGet, "/api/v1/flows?"+test.query, ""))
			assertStatus(t, response, http.StatusOK)
			if calls != 1 {
				t.Fatalf("Flows calls = %d, want 1", calls)
			}
		})
	}

	invalid := []string{
		"limit=",
		"limit=0",
		"limit=5001",
		"limit=-1",
		"limit=1.5",
		"limit=one",
		"limit=1&limit=2",
		"unknown=1",
		"Limit=1",
		"limit=%zz",
		"limit=1;unknown=2",
	}
	for _, query := range invalid {
		t.Run("invalid/"+query, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{flowsFunc: func(context.Context, FlowPageRequest) (FlowPage, error) {
				calls++
				return FlowPage{}, nil
			}})
			request := localRequestFor(http.MethodGet, "/api/v1/flows", "")
			request.URL.RawQuery = query
			response := serve(handler, request)
			assertProblem(t, response, http.StatusBadRequest, "invalid_query")
			if calls != 0 {
				t.Fatalf("Flows calls = %d, want 0", calls)
			}
		})
	}
}

func TestFlowRouteMethodMatrix(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	methods := []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	}
	routes := []struct {
		name    string
		path    string
		allow   string
		allowed map[string]bool
	}{
		{name: "flows", path: "/api/v1/flows", allow: "GET, HEAD", allowed: map[string]bool{http.MethodGet: true, http.MethodHead: true}},
		{name: "mute", path: "/api/v1/flows/mute", allow: "POST", allowed: map[string]bool{http.MethodPost: true}},
		{name: "solo", path: "/api/v1/flows/solo", allow: "POST", allowed: map[string]bool{http.MethodPost: true}},
	}
	for _, route := range routes {
		for _, method := range methods {
			t.Run(route.name+"/"+method, func(t *testing.T) {
				request := localRequestFor(method, route.path, `{"flow_ids":[]}`)
				if method == http.MethodPost {
					request.Header.Set("Content-Type", "application/json")
				}
				response := serve(handler, request)
				wantStatus := http.StatusMethodNotAllowed
				wantAllow := route.allow
				if route.allowed[method] {
					wantStatus = http.StatusOK
					wantAllow = ""
				}
				assertStatus(t, response, wantStatus)
				assertSecurityHeaders(t, response)
				if got := response.Header().Get("Allow"); got != wantAllow {
					t.Fatalf("Allow = %q, want %q", got, wantAllow)
				}
				if method == http.MethodHead {
					if response.Body.Len() != 0 {
						t.Fatalf("HEAD body = %q, want empty", response.Body.String())
					}
					if response.Header().Get("Content-Length") == "" {
						t.Fatal("HEAD Content-Length is empty")
					}
				}
			})
		}
	}

	for _, path := range []string{
		"/api/v1/flows/",
		"/api/v1/flows/mute/",
		"/api/v1/flows/solo/",
		"/api/v1/%66lows",
		"/api/v1/flows%2fmute",
	} {
		t.Run("exact path/"+path, func(t *testing.T) {
			assertProblem(t, serve(handler, localRequestFor(http.MethodGet, path, "")), http.StatusNotFound, "not_found")
		})
	}
}

func TestSetFlowOverlay(t *testing.T) {
	wantIDs := []string{testFlowA, testFlowB}
	wantOverlay := FlowOverlay{Muted: []string{testFlowA}, Soloed: []string{testFlowB}}
	tests := []struct {
		name string
		path string
		stub func(*stubBackend, func(context.Context, []string) (FlowOverlay, error))
	}{
		{name: "mute", path: "/api/v1/flows/mute", stub: func(backend *stubBackend, call func(context.Context, []string) (FlowOverlay, error)) {
			backend.mutedFunc = call
		}},
		{name: "solo", path: "/api/v1/flows/solo", stub: func(backend *stubBackend, call func(context.Context, []string) (FlowOverlay, error)) {
			backend.soloedFunc = call
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			backend := &stubBackend{}
			test.stub(backend, func(ctx context.Context, flowIDs []string) (FlowOverlay, error) {
				calls++
				if ctx == nil {
					t.Fatal("overlay context = nil")
				}
				if !reflect.DeepEqual(flowIDs, wantIDs) {
					t.Fatalf("flow IDs = %#v, want %#v", flowIDs, wantIDs)
				}
				return wantOverlay, nil
			})
			request := localRequestFor(http.MethodPost, test.path, `{"flow_ids":["`+testFlowA+`","`+testFlowB+`"]}`)
			request.Header.Set("Content-Type", "application/json; charset=utf-8")
			response := serve(mustHandler(t, backend), request)
			assertStatus(t, response, http.StatusOK)
			assertSecurityHeaders(t, response)
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := response.Header().Get("ETag"); got != "" {
				t.Fatalf("ETag = %q, want empty for ephemeral overlay", got)
			}
			var got FlowOverlay
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode overlay: %v", err)
			}
			if !reflect.DeepEqual(got, wantOverlay) {
				t.Fatalf("overlay = %#v, want %#v", got, wantOverlay)
			}
			if calls != 1 {
				t.Fatalf("overlay calls = %d, want 1", calls)
			}
		})
	}
}

func TestSetFlowOverlayAcceptsEmptyArray(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{mutedFunc: func(_ context.Context, flowIDs []string) (FlowOverlay, error) {
		calls++
		if flowIDs == nil || len(flowIDs) != 0 {
			t.Fatalf("flow IDs = %#v, want non-nil empty slice", flowIDs)
		}
		return FlowOverlay{Muted: []string{}, Soloed: []string{}}, nil
	}})
	request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", `{"flow_ids":[]}`)
	request.Header.Set("Content-Type", "application/json")
	assertStatus(t, serve(handler, request), http.StatusOK)
	if calls != 1 {
		t.Fatalf("SetMutedFlows calls = %d, want 1", calls)
	}
}

func TestSetFlowOverlayRequiresArray(t *testing.T) {
	for _, body := range []string{`{}`, `{"flow_ids":null}`, `null`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{mutedFunc: func(context.Context, []string) (FlowOverlay, error) {
				calls++
				return FlowOverlay{}, nil
			}})
			request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", body)
			request.Header.Set("Content-Type", "application/json")
			assertProblem(t, serve(handler, request), http.StatusUnprocessableEntity, "invalid_flow_set")
			if calls != 0 {
				t.Fatalf("SetMutedFlows calls = %d, want 0", calls)
			}
		})
	}
}

func TestSetFlowOverlayRejectsDuplicateFlowIDs(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{mutedFunc: func(context.Context, []string) (FlowOverlay, error) {
		calls++
		return FlowOverlay{}, nil
	}})
	request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", `{"flow_ids":["`+testFlowA+`","`+testFlowA+`"]}`)
	request.Header.Set("Content-Type", "application/json")
	assertProblem(t, serve(handler, request), http.StatusUnprocessableEntity, "invalid_flow_set")
	if calls != 0 {
		t.Fatalf("SetMutedFlows calls = %d, want 0", calls)
	}
}

func TestFlowOverlayStrictJSON(t *testing.T) {
	deep := `{"flow_ids":` + strings.Repeat("[", maximumJSONDepth+1) + strings.Repeat("]", maximumJSONDepth+1) + `}`
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "malformed", body: `{"flow_ids":[}`},
		{name: "unknown field", body: `{"flow_ids":[],"unknown":true}`},
		{name: "case-variant field", body: `{"FLOW_IDS":[]}`},
		{name: "duplicate top-level name", body: `{"flow_ids":[],"flow_ids":[]}`},
		{name: "case-variant duplicate", body: `{"flow_ids":["` + testFlowA + `"],"FLOW_IDS":[]}`},
		{name: "duplicate nested name", body: `{"flow_ids":[],"unknown":{"value":1,"value":2}}`},
		{name: "trailing value", body: `{"flow_ids":[]} {}`},
		{name: "top-level array", body: `[]`},
		{name: "wrong field type", body: `{"flow_ids":"` + testFlowA + `"}`},
		{name: "excessive nesting", body: deep},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{mutedFunc: func(context.Context, []string) (FlowOverlay, error) {
				calls++
				return FlowOverlay{}, nil
			}})
			request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", test.body)
			request.Header.Set("Content-Type", "application/json")
			assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
			if calls != 0 {
				t.Fatalf("SetMutedFlows calls = %d, want 0", calls)
			}
		})
	}
}

func TestFlowOverlayRejectsInvalidUTF8(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{mutedFunc: func(context.Context, []string) (FlowOverlay, error) {
		calls++
		return FlowOverlay{}, nil
	}})
	body := string(append([]byte(`{"flow_ids":["`), append([]byte{0xff}, []byte(`"]}`)...)...))
	request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", body)
	request.Header.Set("Content-Type", "application/json")
	assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
	if calls != 0 {
		t.Fatalf("SetMutedFlows calls = %d, want 0", calls)
	}
}

func TestFlowOverlayMediaType(t *testing.T) {
	tests := []struct {
		name        string
		contentType []string
		want        int
	}{
		{name: "missing", want: 415},
		{name: "yaml", contentType: []string{"application/yaml"}, want: 415},
		{name: "text JSON", contentType: []string{"text/json"}, want: 415},
		{name: "malformed", contentType: []string{"application/json garbage"}, want: 415},
		{name: "wrong charset", contentType: []string{"application/json; charset=iso-8859-1"}, want: 415},
		{name: "unknown parameter", contentType: []string{"application/json; profile=flow"}, want: 415},
		{name: "duplicate", contentType: []string{"application/json", "application/json"}, want: 415},
		{name: "exact", contentType: []string{"application/json"}, want: 200},
		{name: "UTF-8", contentType: []string{"application/json; charset=UTF-8"}, want: 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", `{"flow_ids":[]}`)
			for _, contentType := range test.contentType {
				request.Header.Add("Content-Type", contentType)
			}
			response := serve(mustHandler(t, &stubBackend{}), request)
			if test.want == http.StatusOK {
				assertStatus(t, response, test.want)
			} else {
				assertProblem(t, response, test.want, "unsupported_media_type")
			}
		})
	}
}

func TestFlowOverlayContentEncoding(t *testing.T) {
	tests := []struct {
		name     string
		encoding []string
		want     int
	}{
		{name: "absent", want: 200},
		{name: "identity", encoding: []string{"identity"}, want: 200},
		{name: "identity case", encoding: []string{" Identity "}, want: 200},
		{name: "gzip", encoding: []string{"gzip"}, want: 415},
		{name: "list", encoding: []string{"identity, gzip"}, want: 415},
		{name: "duplicate", encoding: []string{"identity", "identity"}, want: 415},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, "/api/v1/flows/solo", `{"flow_ids":[]}`)
			request.Header.Set("Content-Type", "application/json")
			for _, encoding := range test.encoding {
				request.Header.Add("Content-Encoding", encoding)
			}
			response := serve(mustHandler(t, &stubBackend{}), request)
			if test.want == http.StatusOK {
				assertStatus(t, response, test.want)
			} else {
				assertProblem(t, response, test.want, "unsupported_content_encoding")
			}
		})
	}
}

func TestFlowOverlayBodyLimit(t *testing.T) {
	base := `{"flow_ids":[]}`
	exact := base + strings.Repeat(" ", config.MaximumBytes-len(base))
	handler := mustHandler(t, &stubBackend{})

	request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", exact)
	request.Header.Set("Content-Type", "application/json")
	assertStatus(t, serve(handler, request), http.StatusOK)

	request = localRequestFor(http.MethodPost, "/api/v1/flows/mute", exact+" ")
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	assertProblem(t, serve(handler, request), http.StatusRequestEntityTooLarge, "body_too_large")
}

func TestFlowOverlayReadFailure(t *testing.T) {
	request := localRequestFor(http.MethodPost, "/api/v1/flows/mute", "")
	request.Header.Set("Content-Type", "application/json")
	request.Body = errorReader{}
	assertProblem(t, serve(mustHandler(t, &stubBackend{}), request), http.StatusBadRequest, "invalid_body")
}

func TestFlowBackendErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
		err  error
		want int
		code string
	}{
		{name: "list unavailable", path: "/api/v1/flows", err: &BackendError{Kind: ErrorUnavailable, Err: errors.New("private detail")}, want: 503, code: "unavailable"},
		{name: "list generic", path: "/api/v1/flows", err: errors.New("private detail"), want: 500, code: "internal_error"},
		{name: "mute invalid", path: "/api/v1/flows/mute", err: &BackendError{Kind: ErrorInvalid, Code: "invalid_flow_id", Detail: "flow ID is invalid"}, want: 422, code: "invalid_flow_id"},
		{name: "solo unavailable", path: "/api/v1/flows/solo", err: &BackendError{Kind: ErrorUnavailable, Err: errors.New("private detail")}, want: 503, code: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			backend := &stubBackend{}
			switch test.path {
			case "/api/v1/flows":
				backend.flowsFunc = func(context.Context, FlowPageRequest) (FlowPage, error) {
					calls++
					return FlowPage{}, test.err
				}
			case "/api/v1/flows/mute":
				backend.mutedFunc = func(context.Context, []string) (FlowOverlay, error) {
					calls++
					return FlowOverlay{}, test.err
				}
			case "/api/v1/flows/solo":
				backend.soloedFunc = func(context.Context, []string) (FlowOverlay, error) {
					calls++
					return FlowOverlay{}, test.err
				}
			}
			method := http.MethodGet
			body := ""
			if test.path != "/api/v1/flows" {
				method = http.MethodPost
				body = `{"flow_ids":[]}`
			}
			request := localRequestFor(method, test.path, body)
			if method == http.MethodPost {
				request.Header.Set("Content-Type", "application/json")
			}
			response := serve(mustHandler(t, backend), request)
			assertProblem(t, response, test.want, test.code)
			if strings.Contains(response.Body.String(), "private detail") {
				t.Fatalf("response leaked backend detail: %s", response.Body.String())
			}
			if calls != 1 {
				t.Fatalf("backend calls = %d, want 1", calls)
			}
		})
	}
}

func TestFlowsHeadBackendErrorHasNoBody(t *testing.T) {
	handler := mustHandler(t, &stubBackend{flowsFunc: func(context.Context, FlowPageRequest) (FlowPage, error) {
		return FlowPage{}, &BackendError{Kind: ErrorUnavailable}
	}})
	response := serve(handler, localRequestFor(http.MethodHead, "/api/v1/flows", ""))
	assertStatus(t, response, http.StatusServiceUnavailable)
	assertSecurityHeaders(t, response)
	if response.Body.Len() != 0 {
		t.Fatalf("HEAD error body = %q, want empty", response.Body.String())
	}
	if response.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD error Content-Length is empty")
	}
}
