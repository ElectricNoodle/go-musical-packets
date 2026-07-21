package managementapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

const (
	testRevisionA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRevisionB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type stubBackend struct {
	statusFunc       func(context.Context) (Status, error)
	configFunc       func(context.Context) (ConfigDocument, error)
	validateFunc     func(context.Context, config.Config) (Validation, error)
	updateFunc       func(context.Context, Revision, config.Config) (ConfigDocument, error)
	rulesFunc        func(context.Context) (RulesDocument, error)
	createRuleFunc   func(context.Context, Revision, config.RuleConfig) (RulesDocument, error)
	replaceRuleFunc  func(context.Context, Revision, string, config.RuleConfig) (RulesDocument, error)
	deleteRuleFunc   func(context.Context, Revision, string) (RulesDocument, error)
	reorderRulesFunc func(context.Context, Revision, []string) (RulesDocument, error)
	flowsFunc        func(context.Context, FlowPageRequest) (FlowPage, error)
	mutedFunc        func(context.Context, []string) (FlowOverlay, error)
	soloedFunc       func(context.Context, []string) (FlowOverlay, error)
}

func (backend *stubBackend) Status(ctx context.Context) (Status, error) {
	if backend.statusFunc != nil {
		return backend.statusFunc(ctx)
	}
	return Status{}, nil
}

func (backend *stubBackend) Config(ctx context.Context) (ConfigDocument, error) {
	if backend.configFunc != nil {
		return backend.configFunc(ctx)
	}
	return ConfigDocument{Config: config.Default(), Revision: testRevisionA}, nil
}

func (backend *stubBackend) ValidateConfig(ctx context.Context, configuration config.Config) (Validation, error) {
	if backend.validateFunc != nil {
		return backend.validateFunc(ctx, configuration)
	}
	return Validation{Revision: testRevisionA}, nil
}

func (backend *stubBackend) UpdateConfig(ctx context.Context, expected Revision, configuration config.Config) (ConfigDocument, error) {
	if backend.updateFunc != nil {
		return backend.updateFunc(ctx, expected, configuration)
	}
	return ConfigDocument{Config: configuration, Revision: testRevisionB}, nil
}

func (backend *stubBackend) Rules(ctx context.Context) (RulesDocument, error) {
	if backend.rulesFunc != nil {
		return backend.rulesFunc(ctx)
	}
	return RulesDocument{Revision: testRevisionA, Writable: true, Rules: config.RulesConfig{}}, nil
}

func (backend *stubBackend) CreateRule(ctx context.Context, expected Revision, rule config.RuleConfig) (RulesDocument, error) {
	if backend.createRuleFunc != nil {
		return backend.createRuleFunc(ctx, expected, rule)
	}
	return RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{rule}}, nil
}

func (backend *stubBackend) ReplaceRule(ctx context.Context, expected Revision, id string, rule config.RuleConfig) (RulesDocument, error) {
	if backend.replaceRuleFunc != nil {
		return backend.replaceRuleFunc(ctx, expected, id, rule)
	}
	return RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{rule}}, nil
}

func (backend *stubBackend) DeleteRule(ctx context.Context, expected Revision, id string) (RulesDocument, error) {
	if backend.deleteRuleFunc != nil {
		return backend.deleteRuleFunc(ctx, expected, id)
	}
	return RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{}}, nil
}

func (backend *stubBackend) ReorderRules(ctx context.Context, expected Revision, order []string) (RulesDocument, error) {
	if backend.reorderRulesFunc != nil {
		return backend.reorderRulesFunc(ctx, expected, order)
	}
	return RulesDocument{Revision: testRevisionB, Writable: true, Rules: config.RulesConfig{}}, nil
}

func (backend *stubBackend) Flows(ctx context.Context, request FlowPageRequest) (FlowPage, error) {
	if backend.flowsFunc != nil {
		return backend.flowsFunc(ctx, request)
	}
	return FlowPage{Flows: []FlowSnapshot{}, Overlay: FlowOverlay{Muted: []string{}, Soloed: []string{}}, Limit: request.Limit}, nil
}

func (backend *stubBackend) SetMutedFlows(ctx context.Context, flowIDs []string) (FlowOverlay, error) {
	if backend.mutedFunc != nil {
		return backend.mutedFunc(ctx, flowIDs)
	}
	return FlowOverlay{Muted: append([]string(nil), flowIDs...), Soloed: []string{}}, nil
}

func (backend *stubBackend) SetSoloedFlows(ctx context.Context, flowIDs []string) (FlowOverlay, error) {
	if backend.soloedFunc != nil {
		return backend.soloedFunc(ctx, flowIDs)
	}
	return FlowOverlay{Muted: []string{}, Soloed: append([]string(nil), flowIDs...)}, nil
}

func TestNewHandlerRejectsNilBackend(t *testing.T) {
	if _, err := NewHandler(nil, Options{AllowedPort: 8080}); err == nil {
		t.Fatal("NewHandler(nil) error = nil, want error")
	}
	var backend *stubBackend
	if _, err := NewHandler(backend, Options{AllowedPort: 8080}); err == nil {
		t.Fatal("NewHandler(typed nil) error = nil, want error")
	}
}

func TestNewHandlerRequiresAllowedPort(t *testing.T) {
	if _, err := NewHandler(&stubBackend{}, Options{}); err == nil {
		t.Fatal("NewHandler() error = nil, want missing port error")
	}
}

func TestStatusAndHead(t *testing.T) {
	want := Status{State: "ready", Revision: testRevisionA, Writable: true, Warning: "careful"}
	calls := 0
	handler := mustHandler(t, &stubBackend{statusFunc: func(ctx context.Context) (Status, error) {
		calls++
		if ctx == nil {
			t.Fatal("Status context = nil")
		}
		return want, nil
	}})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := serve(handler, localRequestFor(method, "/api/v1/status", ""))
			assertStatus(t, response, http.StatusOK)
			assertSecurityHeaders(t, response)
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if method == http.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q, want empty", response.Body.String())
				}
				if response.Header().Get("Content-Length") == "" {
					t.Fatal("HEAD Content-Length is empty")
				}
				return
			}
			var got Status
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode status: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("status = %#v, want %#v", got, want)
			}
		})
	}
	if calls != 2 {
		t.Fatalf("Status calls = %d, want 2", calls)
	}
}

func TestConfigReturnsCanonicalYAMLAndStrongETag(t *testing.T) {
	configuration := config.Default()
	configuration.Mapping.DefaultChannel = 7
	document := ConfigDocument{Config: configuration, Revision: testRevisionA}
	wantBody, err := config.Encode(configuration)
	if err != nil {
		t.Fatalf("config.Encode() error = %v", err)
	}
	handler := mustHandler(t, &stubBackend{configFunc: func(context.Context) (ConfigDocument, error) {
		return document, nil
	}})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := serve(handler, localRequestFor(method, "/api/v1/config", ""))
			assertStatus(t, response, http.StatusOK)
			assertSecurityHeaders(t, response)
			if got := response.Header().Get("Content-Type"); got != "application/yaml" {
				t.Fatalf("Content-Type = %q, want application/yaml", got)
			}
			if got := response.Header().Get("ETag"); got != `"`+testRevisionA+`"` {
				t.Fatalf("ETag = %q, want strong quoted revision", got)
			}
			if method == http.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q, want empty", response.Body.String())
				}
				return
			}
			if got := response.Body.Bytes(); !reflect.DeepEqual(got, wantBody) {
				t.Fatalf("body = %q, want canonical YAML %q", got, wantBody)
			}
		})
	}
}

func TestConfigRejectsInvalidBackendDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document ConfigDocument
	}{
		{name: "invalid config", document: ConfigDocument{Config: config.Config{}, Revision: testRevisionA}},
		{name: "empty revision", document: ConfigDocument{Config: config.Default()}},
		{name: "invalid revision", document: ConfigDocument{Config: config.Default(), Revision: "bad-revision"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, &stubBackend{configFunc: func(context.Context) (ConfigDocument, error) {
				return test.document, nil
			}})
			response := serve(handler, localRequestFor(http.MethodGet, "/api/v1/config", ""))
			assertProblem(t, response, http.StatusInternalServerError, "internal_error")
		})
	}
}

func TestValidateConfigDecodesStrictRawYAML(t *testing.T) {
	var received config.Config
	handler := mustHandler(t, &stubBackend{validateFunc: func(_ context.Context, configuration config.Config) (Validation, error) {
		received = configuration
		return Validation{
			Revision:              testRevisionA,
			HotFields:             []string{"mapping.default_channel"},
			RestartRequiredFields: []string{"capture.interface"},
		}, nil
	}})
	request := localRequestFor(http.MethodPost, "/api/v1/config/validate", "mapping:\n  default_channel: 9\n")
	request.Header.Set("Content-Type", "application/yaml; charset=utf-8")
	response := serve(handler, request)

	assertStatus(t, response, http.StatusOK)
	if received.Mapping.DefaultChannel != 9 || received.Mapping.Version != config.Default().Mapping.Version {
		t.Fatalf("received config = %#v, want strict document overlaid on defaults", received)
	}
	var got Validation
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode validation: %v", err)
	}
	if !reflect.DeepEqual(got.HotFields, []string{"mapping.default_channel"}) || !reflect.DeepEqual(got.RestartRequiredFields, []string{"capture.interface"}) {
		t.Fatalf("validation = %#v", got)
	}
	if got.Revision != testRevisionA {
		t.Fatalf("validation revision = %q, want %q", got.Revision, testRevisionA)
	}
	if gotETag := response.Header().Get("ETag"); gotETag != "" {
		t.Fatalf("validation ETag = %q, want empty because the response varies by candidate", gotETag)
	}
}

func TestConfigRequestContentType(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	for _, contentType := range []string{"", "text/yaml", "application/json", "application/yaml garbage", "application/yaml; charset=iso-8859-1", "application/yaml; profile=full"} {
		t.Run(contentType, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, "/api/v1/config/validate", "{}\n")
			if contentType != "" {
				request.Header.Set("Content-Type", contentType)
			}
			response := serve(handler, request)
			assertProblem(t, response, http.StatusUnsupportedMediaType, "unsupported_media_type")
		})
	}
}

func TestConfigRequestRejectsMultipleContentTypes(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	request := localRequestFor(http.MethodPost, "/api/v1/config/validate", "{}\n")
	request.Header.Add("Content-Type", "application/yaml")
	request.Header.Add("Content-Type", "application/yaml")
	assertProblem(t, serve(handler, request), http.StatusUnsupportedMediaType, "unsupported_media_type")
}

func TestConfigRequestContentEncoding(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	for _, test := range []struct {
		name     string
		encoding []string
		status   int
	}{
		{name: "absent", status: 200},
		{name: "identity", encoding: []string{"identity"}, status: 200},
		{name: "identity case", encoding: []string{" Identity "}, status: 200},
		{name: "gzip", encoding: []string{"gzip"}, status: 415},
		{name: "list", encoding: []string{"identity, gzip"}, status: 415},
		{name: "duplicate", encoding: []string{"identity", "identity"}, status: 415},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, "/api/v1/config/validate", "{}\n")
			request.Header.Set("Content-Type", "application/yaml")
			for _, encoding := range test.encoding {
				request.Header.Add("Content-Encoding", encoding)
			}
			response := serve(handler, request)
			if test.status == 200 {
				assertStatus(t, response, test.status)
			} else {
				assertProblem(t, response, test.status, "unsupported_content_encoding")
			}
		})
	}
}

func TestConfigRequestRejectsInvalidYAML(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "syntax", body: "mapping: [\n"},
		{name: "unknown field", body: "unknown: true\n"},
		{name: "multiple documents", body: "{}\n---\n{}\n"},
		{name: "invalid value", body: "mapping:\n  default_channel: 99\n"},
	}
	handler := mustHandler(t, &stubBackend{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, "/api/v1/config/validate", test.body)
			request.Header.Set("Content-Type", "application/yaml")
			response := serve(handler, request)
			assertProblem(t, response, http.StatusUnprocessableEntity, "invalid_config")
		})
	}
}

func TestConfigRequestSizeLimit(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	request := localRequestFor(http.MethodPost, "/api/v1/config/validate", strings.Repeat(" ", config.MaximumBytes+1))
	request.Header.Set("Content-Type", "application/yaml")
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	response := serve(handler, request)
	assertProblem(t, response, http.StatusRequestEntityTooLarge, "body_too_large")

	request = localRequestFor(http.MethodPost, "/api/v1/config/validate", strings.Repeat(" ", config.MaximumBytes))
	request.Header.Set("Content-Type", "application/yaml")
	response = serve(handler, request)
	assertStatus(t, response, http.StatusOK)
}

func TestConfigRequestReadFailure(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	request := localRequestFor(http.MethodPost, "/api/v1/config/validate", "")
	request.Header.Set("Content-Type", "application/yaml")
	request.Body = errorReader{}
	response := serve(handler, request)
	assertProblem(t, response, http.StatusBadRequest, "invalid_body")
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errorReader) Close() error             { return nil }

func TestUpdateConfigUsesStrongPrecondition(t *testing.T) {
	var gotExpected Revision
	var gotConfig config.Config
	handler := mustHandler(t, &stubBackend{updateFunc: func(_ context.Context, expected Revision, configuration config.Config) (ConfigDocument, error) {
		gotExpected = expected
		gotConfig = configuration
		return ConfigDocument{Config: configuration, Revision: testRevisionB}, nil
	}})
	request := localRequestFor(http.MethodPut, "/api/v1/config", "mapping:\n  default_channel: 4\n")
	request.Header.Set("Content-Type", "application/yaml")
	request.Header.Set("If-Match", `"`+testRevisionA+`"`)
	response := serve(handler, request)

	assertStatus(t, response, http.StatusOK)
	if gotExpected != testRevisionA {
		t.Fatalf("expected revision = %q, want %q", gotExpected, testRevisionA)
	}
	if gotConfig.Mapping.DefaultChannel != 4 {
		t.Fatalf("default channel = %d, want 4", gotConfig.Mapping.DefaultChannel)
	}
	if got := response.Header().Get("ETag"); got != `"`+testRevisionB+`"` {
		t.Fatalf("ETag = %q, want next", got)
	}
	if got := response.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("Content-Type = %q, want application/yaml", got)
	}
}

func TestPartialPutResetsOmittedFieldsToDefaults(t *testing.T) {
	var got config.Config
	handler := mustHandler(t, &stubBackend{updateFunc: func(_ context.Context, _ Revision, configuration config.Config) (ConfigDocument, error) {
		got = configuration
		return ConfigDocument{Config: configuration, Revision: testRevisionB}, nil
	}})
	request := localRequestFor(http.MethodPut, "/api/v1/config", "mapping:\n  default_channel: 12\n")
	request.Header.Set("Content-Type", "application/yaml")
	request.Header.Set("If-Match", `"`+testRevisionA+`"`)
	response := serve(handler, request)
	assertStatus(t, response, http.StatusOK)

	defaults := config.Default()
	if got.Mapping.DefaultChannel != 12 {
		t.Fatalf("default channel = %d, want 12", got.Mapping.DefaultChannel)
	}
	got.Mapping.DefaultChannel = defaults.Mapping.DefaultChannel
	if !reflect.DeepEqual(got, defaults) {
		t.Fatalf("partial PUT = %#v, want omitted fields reset to defaults %#v", got, defaults)
	}
}

func TestUpdateConfigRequiresIfMatch(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	request := localRequestFor(http.MethodPut, "/api/v1/config", "{}\n")
	request.Header.Set("Content-Type", "application/yaml")
	response := serve(handler, request)
	assertProblem(t, response, http.StatusPreconditionRequired, "precondition_required")
}

func TestUpdateConfigRejectsMalformedIfMatch(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "unquoted", values: []string{testRevisionA}},
		{name: "weak", values: []string{`W/"` + testRevisionA + `"`}},
		{name: "wildcard", values: []string{"*"}},
		{name: "empty", values: []string{`""`}},
		{name: "short", values: []string{`"current"`}},
		{name: "uppercase", values: []string{`"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`}},
		{name: "list", values: []string{`"one", "two"`}},
		{name: "duplicate fields", values: []string{`"one"`, `"two"`}},
		{name: "control", values: []string{"\"one\x7ftwo\""}},
	}
	handler := mustHandler(t, &stubBackend{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPut, "/api/v1/config", "{}\n")
			request.Header.Set("Content-Type", "application/yaml")
			for _, value := range test.values {
				request.Header.Add("If-Match", value)
			}
			response := serve(handler, request)
			assertProblem(t, response, http.StatusBadRequest, "invalid_if_match")
		})
	}
}

func TestBackendErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		fields []string
	}{
		{name: "invalid", err: &BackendError{Kind: ErrorInvalid, Detail: "not valid"}, status: 422, code: "invalid"},
		{name: "invalid cause only", err: &BackendError{Kind: ErrorInvalid, Err: errors.New("secret invalid cause")}, status: 422, code: "invalid"},
		{name: "precondition", err: &BackendError{Kind: ErrorPreconditionFailed, Detail: "stale", ActualRevision: testRevisionB}, status: 412, code: "precondition_failed"},
		{name: "precondition cause only", err: &BackendError{Kind: ErrorPreconditionFailed, Err: errors.New("secret precondition cause")}, status: 412, code: "precondition_failed"},
		{name: "conflict", err: &BackendError{Kind: ErrorConflict, Code: "restart_required", Detail: "restart", Fields: []string{"capture.interface"}}, status: 409, code: "restart_required", fields: []string{"capture.interface"}},
		{name: "conflict cause only", err: &BackendError{Kind: ErrorConflict, Err: errors.New("secret conflict cause")}, status: 409, code: "conflict"},
		{name: "not found", err: &BackendError{Kind: ErrorNotFound, Detail: "missing"}, status: 404, code: "not_found"},
		{name: "unavailable", err: &BackendError{Kind: ErrorUnavailable, Err: errors.New("disk failed")}, status: 503, code: "unavailable"},
		{name: "wrapped", err: errors.Join(errors.New("outer"), &BackendError{Kind: ErrorConflict, Detail: "busy"}), status: 409, code: "conflict"},
		{name: "generic", err: errors.New("secret backend detail"), status: 500, code: "internal_error"},
		{name: "typed nil", err: (*BackendError)(nil), status: 500, code: "internal_error"},
		{name: "unknown kind", err: &BackendError{Kind: "mystery", Detail: "secret backend detail"}, status: 500, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, &stubBackend{statusFunc: func(context.Context) (Status, error) {
				return Status{}, test.err
			}})
			response := serve(handler, localRequestFor(http.MethodGet, "/api/v1/status", ""))
			got := assertProblem(t, response, test.status, test.code)
			if !reflect.DeepEqual(got.Fields, test.fields) {
				t.Fatalf("fields = %#v, want %#v", got.Fields, test.fields)
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "disk failed") {
				t.Fatalf("response leaked backend error: %s", response.Body.String())
			}
			if test.name == "precondition" && response.Header().Get("ETag") != `"`+testRevisionB+`"` {
				t.Fatalf("ETag = %q, want actual revision", response.Header().Get("ETag"))
			}
		})
	}
}

func TestBackendErrorsFromConfigOperations(t *testing.T) {
	backendErr := &BackendError{Kind: ErrorUnavailable, Detail: "temporarily unavailable"}
	backend := &stubBackend{
		configFunc: func(context.Context) (ConfigDocument, error) { return ConfigDocument{}, backendErr },
		validateFunc: func(context.Context, config.Config) (Validation, error) {
			return Validation{}, backendErr
		},
		updateFunc: func(context.Context, Revision, config.Config) (ConfigDocument, error) {
			return ConfigDocument{}, backendErr
		},
	}
	handler := mustHandler(t, backend)

	getResponse := serve(handler, localRequestFor(http.MethodGet, "/api/v1/config", ""))
	assertProblem(t, getResponse, http.StatusServiceUnavailable, "unavailable")

	validateRequest := localRequestFor(http.MethodPost, "/api/v1/config/validate", "{}\n")
	validateRequest.Header.Set("Content-Type", "application/yaml")
	assertProblem(t, serve(handler, validateRequest), http.StatusServiceUnavailable, "unavailable")

	updateRequest := localRequestFor(http.MethodPut, "/api/v1/config", "{}\n")
	updateRequest.Header.Set("Content-Type", "application/yaml")
	updateRequest.Header.Set("If-Match", `"`+testRevisionA+`"`)
	assertProblem(t, serve(handler, updateRequest), http.StatusServiceUnavailable, "unavailable")
}

func TestLocalRequestSecurity(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{statusFunc: func(context.Context) (Status, error) {
		calls++
		return Status{}, nil
	}})
	tests := []struct {
		name       string
		remoteAddr string
		host       string
		origin     []string
		tls        bool
		want       int
	}{
		{name: "IPv4 loopback", remoteAddr: "127.0.0.1:1234", host: "127.0.0.1:8080", want: 200},
		{name: "IPv6 loopback", remoteAddr: "[::1]:1234", host: "[::1]:8080", want: 200},
		{name: "localhost", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", want: 200},
		{name: "matching origin", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"http://localhost:8080"}, want: 200},
		{name: "matching TLS origin", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"https://localhost:8080"}, tls: true, want: 200},
		{name: "remote client", remoteAddr: "192.0.2.10:1234", host: "localhost:8080", want: 403},
		{name: "unparseable client", remoteAddr: "localhost:1234", host: "localhost:8080", want: 403},
		{name: "client port absent", remoteAddr: "127.0.0.1", host: "localhost:8080", want: 403},
		{name: "client port invalid", remoteAddr: "127.0.0.1:not-a-port", host: "localhost:8080", want: 403},
		{name: "client port zero", remoteAddr: "127.0.0.1:0", host: "localhost:8080", want: 403},
		{name: "remote host", remoteAddr: "127.0.0.1:1234", host: "example.com", want: 403},
		{name: "wildcard host", remoteAddr: "127.0.0.1:1234", host: "0.0.0.0:8080", want: 403},
		{name: "localhost suffix", remoteAddr: "127.0.0.1:1234", host: "localhost.example", want: 403},
		{name: "missing non-default port", remoteAddr: "127.0.0.1:1234", host: "localhost", want: 403},
		{name: "wrong port", remoteAddr: "127.0.0.1:1234", host: "localhost:8081", want: 403},
		{name: "empty explicit port", remoteAddr: "127.0.0.1:1234", host: "localhost:", want: 403},
		{name: "unbracketed IPv6 authority", remoteAddr: "127.0.0.1:1234", host: "::1", want: 403},
		{name: "empty host", remoteAddr: "127.0.0.1:1234", want: 403},
		{name: "cross origin host", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"http://127.0.0.1:8080"}, want: 403},
		{name: "cross origin scheme", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"https://localhost:8080"}, want: 403},
		{name: "remote origin", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"http://example.com"}, want: 403},
		{name: "null origin", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"null"}, want: 403},
		{name: "origin path", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"http://localhost:8080/path"}, want: 403},
		{name: "multiple origins", remoteAddr: "127.0.0.1:1234", host: "localhost:8080", origin: []string{"http://localhost:8080", "http://localhost:8080"}, want: 403},
	}
	allowed := 0
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
			request.RemoteAddr = test.remoteAddr
			request.Host = test.host
			for _, origin := range test.origin {
				request.Header.Add("Origin", origin)
			}
			if test.tls {
				request.TLS = &tls.ConnectionState{}
			}
			response := serve(handler, request)
			assertStatus(t, response, test.want)
			assertSecurityHeaders(t, response)
			if test.want == http.StatusForbidden {
				assertProblem(t, response, http.StatusForbidden, "forbidden")
			} else {
				allowed++
			}
		})
	}
	if calls != allowed {
		t.Fatalf("backend calls = %d, want %d allowed requests", calls, allowed)
	}
}

func TestForwardedHeadersDoNotAuthorizeRemoteClient(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{statusFunc: func(context.Context) (Status, error) {
		calls++
		return Status{}, nil
	}})
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Host = "localhost:8080"
	request.Header.Set("Forwarded", "for=127.0.0.1;host=localhost:8080;proto=http")
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	request.Header.Set("X-Forwarded-Host", "localhost:8080")
	request.Header.Set("X-Forwarded-Proto", "http")
	assertProblem(t, serve(handler, request), http.StatusForbidden, "forbidden")
	if calls != 0 {
		t.Fatalf("backend calls = %d, want 0", calls)
	}
}

func TestDefaultPortMayBeOmitted(t *testing.T) {
	for _, test := range []struct {
		name   string
		port   uint16
		host   string
		origin string
		tls    bool
	}{
		{name: "HTTP", port: 80, host: "localhost", origin: "http://localhost"},
		{name: "HTTPS", port: 443, host: "[::1]", origin: "https://[::1]", tls: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewHandler(&stubBackend{}, Options{AllowedPort: test.port})
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/status", nil)
			request.RemoteAddr = "127.0.0.1:1234"
			request.Host = test.host
			request.Header.Set("Origin", test.origin)
			if test.tls {
				request.TLS = &tls.ConnectionState{}
			}
			assertStatus(t, serve(handler, request), http.StatusOK)
		})
	}
}

func TestManualRouting(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	tests := []struct {
		name   string
		method string
		path   string
		status int
		code   string
		allow  string
	}{
		{name: "missing", method: http.MethodGet, path: "/api/v1/missing", status: 404, code: "not_found"},
		{name: "trailing slash", method: http.MethodGet, path: "/api/v1/status/", status: 404, code: "not_found"},
		{name: "encoded slash", method: http.MethodGet, path: "/api%2fv1/status", status: 404, code: "not_found"},
		{name: "encoded letter", method: http.MethodGet, path: "/%61pi/v1/status", status: 404, code: "not_found"},
		{name: "status method", method: http.MethodPost, path: "/api/v1/status", status: 405, code: "method_not_allowed", allow: "GET, HEAD"},
		{name: "config method", method: http.MethodDelete, path: "/api/v1/config", status: 405, code: "method_not_allowed", allow: "GET, HEAD, PUT"},
		{name: "validate method", method: http.MethodGet, path: "/api/v1/config/validate", status: 405, code: "method_not_allowed", allow: "POST"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serve(handler, localRequestFor(test.method, test.path, ""))
			assertProblem(t, response, test.status, test.code)
			if got := response.Header().Get("Allow"); got != test.allow {
				t.Fatalf("Allow = %q, want %q", got, test.allow)
			}
			assertSecurityHeaders(t, response)
		})
	}
}

func TestRouteMethodMatrix(t *testing.T) {
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
		missing bool
	}{
		{name: "status", path: "/api/v1/status", allow: "GET, HEAD", allowed: map[string]bool{http.MethodGet: true, http.MethodHead: true}},
		{name: "config", path: "/api/v1/config", allow: "GET, HEAD, PUT", allowed: map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodPut: true}},
		{name: "validate", path: "/api/v1/config/validate", allow: "POST", allowed: map[string]bool{http.MethodPost: true}},
		{name: "missing", path: "/api/v1/missing", missing: true},
	}

	for _, route := range routes {
		for _, method := range methods {
			t.Run(route.name+"/"+method, func(t *testing.T) {
				request := localRequestFor(method, route.path, "{}\n")
				if method == http.MethodPost || method == http.MethodPut {
					request.Header.Set("Content-Type", "application/yaml")
				}
				if method == http.MethodPut {
					request.Header.Set("If-Match", `"`+testRevisionA+`"`)
				}
				response := serve(handler, request)

				wantStatus := http.StatusMethodNotAllowed
				wantAllow := route.allow
				if route.missing {
					wantStatus = http.StatusNotFound
					wantAllow = ""
				} else if route.allowed[method] {
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
}

func TestHeadProblemHasNoBody(t *testing.T) {
	handler := mustHandler(t, &stubBackend{statusFunc: func(context.Context) (Status, error) {
		return Status{}, &BackendError{Kind: ErrorUnavailable, Detail: "down"}
	}})
	response := serve(handler, localRequestFor(http.MethodHead, "/api/v1/status", ""))
	assertStatus(t, response, http.StatusServiceUnavailable)
	if response.Body.Len() != 0 {
		t.Fatalf("HEAD problem body = %q, want empty", response.Body.String())
	}
	if response.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD problem Content-Length is empty")
	}
}

func mustHandler(t *testing.T, backend Backend) http.Handler {
	t.Helper()
	handler, err := NewHandler(backend, Options{AllowedPort: 8080})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func localRequestFor(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Host = "localhost:8080"
	return request
}

func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, want, response.Body.String())
	}
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) problem {
	t.Helper()
	assertStatus(t, response, status)
	assertSecurityHeaders(t, response)
	if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}
	var got problem
	if response.Body.Len() != 0 {
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode problem: %v; body = %s", err, response.Body.String())
		}
		if got.Status != status || got.Code != code || got.Detail == "" || got.Title == "" || got.Type != "about:blank" {
			t.Fatalf("problem = %#v, want status %d code %q and complete fields", got, status, code)
		}
	}
	return got
}

var _ io.ReadCloser = errorReader{}
