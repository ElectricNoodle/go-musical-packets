package managementapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestMIDIDevicesGetAndHead(t *testing.T) {
	current := MIDIDevice{Number: 2, Name: "USB Synth"}
	want := MIDIDevicesDocument{
		Enabled:   true,
		Discovery: MIDIDiscoveryOK,
		Connected: true,
		Current:   &current,
		Devices: []MIDIDevice{
			{Number: 1, Name: "IAC Driver"},
			current,
		},
	}
	calls := 0
	handler := mustHandler(t, &stubBackend{midiDevicesFunc: func(ctx context.Context) (MIDIDevicesDocument, error) {
		calls++
		if ctx == nil {
			t.Fatal("MIDIDevices context = nil")
		}
		return want, nil
	}})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := serve(handler, localRequestFor(method, midiDevicesPath, ""))
			assertStatus(t, response, http.StatusOK)
			assertSecurityHeaders(t, response)
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := response.Header().Get("ETag"); got != "" {
				t.Fatalf("ETag = %q, want empty for ephemeral MIDI state", got)
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
			assertMIDIDevicesDocument(t, response.Body.Bytes(), want)
		})
	}
	if calls != 2 {
		t.Fatalf("MIDIDevices calls = %d, want 2", calls)
	}
}

func TestMIDIDevicesRepresentsDisabledDisconnectedAndDiscoveryError(t *testing.T) {
	stale := MIDIDevice{Number: 3, Name: "Still Connected"}
	tests := []MIDIDevicesDocument{
		{Discovery: MIDIDiscoveryDisabled, Devices: []MIDIDevice{}},
		{Enabled: true, Discovery: MIDIDiscoveryOK, Devices: []MIDIDevice{}},
		{Enabled: true, Discovery: MIDIDiscoveryError, Devices: []MIDIDevice{}},
		{Enabled: true, Discovery: MIDIDiscoveryError, Connected: true, Current: &stale, Devices: []MIDIDevice{{Number: 1, Name: "Cached"}}},
	}
	for _, want := range tests {
		t.Run(string(want.Discovery)+"/connected="+boolText(want.Connected), func(t *testing.T) {
			handler := mustHandler(t, &stubBackend{midiDevicesFunc: func(context.Context) (MIDIDevicesDocument, error) {
				return want, nil
			}})
			response := serve(handler, localRequestFor(http.MethodGet, midiDevicesPath, ""))
			assertStatus(t, response, http.StatusOK)
			assertMIDIDevicesDocument(t, response.Body.Bytes(), want)
			if strings.Contains(response.Body.String(), `"devices":null`) || strings.Contains(response.Body.String(), `"current"`) && want.Current == nil && !strings.Contains(response.Body.String(), `"current":null`) {
				t.Fatalf("unstable empty representation: %s", response.Body.String())
			}
		})
	}
}

func TestMIDIDevicesRejectsInvalidBackendDocuments(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name     string
		document MIDIDevicesDocument
	}{
		{name: "unknown discovery", document: MIDIDevicesDocument{Enabled: true, Discovery: "mystery", Devices: []MIDIDevice{}}},
		{name: "enabled marked disabled", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryDisabled, Devices: []MIDIDevice{}}},
		{name: "disabled marked discovered", document: MIDIDevicesDocument{Discovery: MIDIDiscoveryOK, Devices: []MIDIDevice{}}},
		{name: "nil devices", document: MIDIDevicesDocument{Discovery: MIDIDiscoveryDisabled}},
		{name: "connected without current", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryOK, Connected: true, Devices: []MIDIDevice{}}},
		{name: "current while disconnected", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryOK, Current: &MIDIDevice{}, Devices: []MIDIDevice{}}},
		{name: "disabled connected", document: MIDIDevicesDocument{Discovery: MIDIDiscoveryDisabled, Connected: true, Current: &MIDIDevice{}, Devices: []MIDIDevice{}}},
		{name: "negative current number", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryOK, Connected: true, Current: &MIDIDevice{Number: -1}, Devices: []MIDIDevice{}}},
		{name: "negative device number", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryOK, Devices: []MIDIDevice{{Number: -1}}}},
		{name: "invalid current name", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryOK, Connected: true, Current: &MIDIDevice{Name: invalidUTF8}, Devices: []MIDIDevice{}}},
		{name: "invalid device name", document: MIDIDevicesDocument{Enabled: true, Discovery: MIDIDiscoveryOK, Devices: []MIDIDevice{{Name: invalidUTF8}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, &stubBackend{midiDevicesFunc: func(context.Context) (MIDIDevicesDocument, error) {
				return test.document, nil
			}})
			response := serve(handler, localRequestFor(http.MethodGet, midiDevicesPath, ""))
			assertProblem(t, response, http.StatusInternalServerError, "internal_error")
		})
	}
}

func TestMIDIAuditionAcceptsBoundedRequestWithoutRevision(t *testing.T) {
	want := MIDIAuditionRequest{Channel: 16, Note: 0, Velocity: 127, DurationMS: 10_000}
	calls := 0
	handler := mustHandler(t, &stubBackend{auditionMIDIFunc: func(ctx context.Context, request MIDIAuditionRequest) error {
		calls++
		if ctx == nil {
			t.Fatal("AuditionMIDI context = nil")
		}
		if !reflect.DeepEqual(request, want) {
			t.Fatalf("AuditionMIDI request = %#v, want %#v", request, want)
		}
		return nil
	}})
	request := localRequestFor(http.MethodPost, midiAuditionPath, `{"channel":16,"note":0,"velocity":127,"duration_ms":10000}`)
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	request.Header.Set("Content-Encoding", " Identity ")
	request.Header.Set("If-Match", `"not-a-config-revision"`)
	response := serve(handler, request)

	assertStatus(t, response, http.StatusAccepted)
	assertSecurityHeaders(t, response)
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type = %q, want empty", got)
	}
	if got := response.Header().Get("Content-Length"); got != "0" {
		t.Fatalf("Content-Length = %q, want 0", got)
	}
	if got := response.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag = %q, want empty", got)
	}
	if calls != 1 {
		t.Fatalf("AuditionMIDI calls = %d, want 1", calls)
	}
}

func TestMIDIAuditionValidatesRequiredFieldRanges(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		fields []string
	}{
		{name: "empty object", body: `{}`, fields: []string{"channel", "note", "velocity", "duration_ms"}},
		{name: "missing channel", body: `{"note":60,"velocity":100,"duration_ms":100}`, fields: []string{"channel"}},
		{name: "missing note", body: `{"channel":1,"velocity":100,"duration_ms":100}`, fields: []string{"note"}},
		{name: "missing velocity", body: `{"channel":1,"note":60,"duration_ms":100}`, fields: []string{"velocity"}},
		{name: "missing duration", body: `{"channel":1,"note":60,"velocity":100}`, fields: []string{"duration_ms"}},
		{name: "null fields", body: `{"channel":null,"note":null,"velocity":null,"duration_ms":null}`, fields: []string{"channel", "note", "velocity", "duration_ms"}},
		{name: "channel below", body: `{"channel":0,"note":60,"velocity":100,"duration_ms":100}`, fields: []string{"channel"}},
		{name: "channel above", body: `{"channel":17,"note":60,"velocity":100,"duration_ms":100}`, fields: []string{"channel"}},
		{name: "note below", body: `{"channel":1,"note":-1,"velocity":100,"duration_ms":100}`, fields: []string{"note"}},
		{name: "note above", body: `{"channel":1,"note":128,"velocity":100,"duration_ms":100}`, fields: []string{"note"}},
		{name: "velocity below", body: `{"channel":1,"note":60,"velocity":0,"duration_ms":100}`, fields: []string{"velocity"}},
		{name: "velocity above", body: `{"channel":1,"note":60,"velocity":128,"duration_ms":100}`, fields: []string{"velocity"}},
		{name: "duration below", body: `{"channel":1,"note":60,"velocity":100,"duration_ms":0}`, fields: []string{"duration_ms"}},
		{name: "duration above", body: `{"channel":1,"note":60,"velocity":100,"duration_ms":10001}`, fields: []string{"duration_ms"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{auditionMIDIFunc: func(context.Context, MIDIAuditionRequest) error {
				calls++
				return nil
			}})
			request := localRequestFor(http.MethodPost, midiAuditionPath, test.body)
			request.Header.Set("Content-Type", "application/json")
			problem := assertProblem(t, serve(handler, request), http.StatusUnprocessableEntity, "invalid_audition")
			if !reflect.DeepEqual(problem.Fields, test.fields) {
				t.Fatalf("fields = %#v, want %#v", problem.Fields, test.fields)
			}
			if calls != 0 {
				t.Fatalf("AuditionMIDI calls = %d, want 0", calls)
			}
		})
	}
}

func TestMIDIAuditionStrictJSON(t *testing.T) {
	deep := strings.Repeat("[", maximumJSONDepth+1) + strings.Repeat("]", maximumJSONDepth+1)
	tests := []string{
		``,
		`null`,
		`[]`,
		`{"channel":1`,
		`{"CHANNEL":1,"note":60,"velocity":100,"duration_ms":100}`,
		`{"channel":1,"Note":60,"velocity":100,"duration_ms":100}`,
		`{"channel":1,"note":60,"VELOCITY":100,"duration_ms":100}`,
		`{"channel":1,"note":60,"velocity":100,"Duration_MS":100}`,
		`{"channel":1,"channel":2,"note":60,"velocity":100,"duration_ms":100}`,
		`{"channel":1,"note":60,"velocity":100,"duration_ms":100,"unknown":true}`,
		`{"channel":1,"note":60,"velocity":100,"duration_ms":100} {}`,
		`{"channel":1.5,"note":60,"velocity":100,"duration_ms":100}`,
		`{"channel":"1","note":60,"velocity":100,"duration_ms":100}`,
		`{"channel":1,"note":60,"velocity":100,"duration_ms":100,"unknown":` + deep + `}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{auditionMIDIFunc: func(context.Context, MIDIAuditionRequest) error {
				calls++
				return nil
			}})
			request := localRequestFor(http.MethodPost, midiAuditionPath, body)
			request.Header.Set("Content-Type", "application/json")
			assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
			if calls != 0 {
				t.Fatalf("AuditionMIDI calls = %d, want 0", calls)
			}
		})
	}
}

func TestMIDIAuditionRejectsInvalidUTF8(t *testing.T) {
	body := string(append([]byte(`{"channel":1,"note":60,"velocity":100,"duration_ms":100,"`), append([]byte{0xff}, []byte(`":true}`)...)...))
	request := localRequestFor(http.MethodPost, midiAuditionPath, body)
	request.Header.Set("Content-Type", "application/json")
	assertProblem(t, serve(mustHandler(t, &stubBackend{}), request), http.StatusBadRequest, "invalid_body")
}

func TestMIDIAuditionMediaTypeAndEncoding(t *testing.T) {
	body := `{"channel":1,"note":60,"velocity":100,"duration_ms":100}`
	tests := []struct {
		name        string
		contentType []string
		encoding    []string
		status      int
		code        string
	}{
		{name: "missing", status: 415, code: "unsupported_media_type"},
		{name: "YAML", contentType: []string{"application/yaml"}, status: 415, code: "unsupported_media_type"},
		{name: "bad charset", contentType: []string{"application/json; charset=latin1"}, status: 415, code: "unsupported_media_type"},
		{name: "parameter", contentType: []string{"application/json; profile=audition"}, status: 415, code: "unsupported_media_type"},
		{name: "duplicate type", contentType: []string{"application/json", "application/json"}, status: 415, code: "unsupported_media_type"},
		{name: "gzip", contentType: []string{"application/json"}, encoding: []string{"gzip"}, status: 415, code: "unsupported_content_encoding"},
		{name: "encoding list", contentType: []string{"application/json"}, encoding: []string{"identity, gzip"}, status: 415, code: "unsupported_content_encoding"},
		{name: "duplicate encoding", contentType: []string{"application/json"}, encoding: []string{"identity", "identity"}, status: 415, code: "unsupported_content_encoding"},
		{name: "exact", contentType: []string{"application/json"}, status: 202},
		{name: "UTF-8 identity", contentType: []string{"application/json; charset=UTF-8"}, encoding: []string{" Identity "}, status: 202},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(http.MethodPost, midiAuditionPath, body)
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

func TestMIDIAuditionBodyLimitAndReadFailure(t *testing.T) {
	base := `{"channel":1,"note":60,"velocity":100,"duration_ms":100}`
	exact := base + strings.Repeat(" ", maximumMIDIAuditionBodyBytes-len(base))
	handler := mustHandler(t, &stubBackend{})

	request := localRequestFor(http.MethodPost, midiAuditionPath, exact)
	request.Header.Set("Content-Type", "application/json")
	assertStatus(t, serve(handler, request), http.StatusAccepted)

	request = localRequestFor(http.MethodPost, midiAuditionPath, exact+" ")
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	assertProblem(t, serve(handler, request), http.StatusRequestEntityTooLarge, "body_too_large")

	request = localRequestFor(http.MethodPost, midiAuditionPath, "")
	request.Header.Set("Content-Type", "application/json")
	request.Body = errorReader{}
	assertProblem(t, serve(handler, request), http.StatusBadRequest, "invalid_body")
}

func TestMIDIPanicRequiresEmptyUnencodedBody(t *testing.T) {
	calls := 0
	handler := mustHandler(t, &stubBackend{panicMIDIFunc: func(ctx context.Context) error {
		calls++
		if ctx == nil {
			t.Fatal("PanicMIDI context = nil")
		}
		return nil
	}})
	request := localRequestFor(http.MethodPost, midiPanicPath, "")
	request.Header.Set("Content-Encoding", " identity ")
	request.Header.Set("If-Match", `"not-a-config-revision"`)
	response := serve(handler, request)

	assertStatus(t, response, http.StatusNoContent)
	assertSecurityHeaders(t, response)
	if response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" || response.Header().Get("ETag") != "" {
		t.Fatalf("panic response body=%q headers=%v", response.Body.String(), response.Header())
	}
	if calls != 1 {
		t.Fatalf("PanicMIDI calls = %d, want 1", calls)
	}
}

func TestMIDIPanicRejectsRepresentation(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType []string
		encoding    []string
		readError   bool
		code        string
		status      int
	}{
		{name: "JSON body", body: `{}`, status: 400, code: "invalid_body"},
		{name: "one byte", body: `x`, status: 400, code: "invalid_body"},
		{name: "content type", contentType: []string{"application/json"}, status: 415, code: "unsupported_media_type"},
		{name: "empty content type", contentType: []string{""}, status: 415, code: "unsupported_media_type"},
		{name: "gzip", encoding: []string{"gzip"}, status: 415, code: "unsupported_content_encoding"},
		{name: "encoding list", encoding: []string{"identity, gzip"}, status: 415, code: "unsupported_content_encoding"},
		{name: "duplicate encoding", encoding: []string{"identity", "identity"}, status: 415, code: "unsupported_content_encoding"},
		{name: "read failure", readError: true, status: 400, code: "invalid_body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler := mustHandler(t, &stubBackend{panicMIDIFunc: func(context.Context) error {
				calls++
				return nil
			}})
			request := localRequestFor(http.MethodPost, midiPanicPath, test.body)
			for _, value := range test.contentType {
				request.Header.Add("Content-Type", value)
			}
			for _, value := range test.encoding {
				request.Header.Add("Content-Encoding", value)
			}
			if test.readError {
				request.Body = errorReader{}
			}
			assertProblem(t, serve(handler, request), test.status, test.code)
			if calls != 0 {
				t.Fatalf("PanicMIDI calls = %d, want 0", calls)
			}
		})
	}
}

func TestMIDIRouteMethodAndQueryMatrix(t *testing.T) {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
	routes := []struct {
		name    string
		path    string
		allow   string
		allowed map[string]int
	}{
		{name: "devices", path: midiDevicesPath, allow: "GET, HEAD", allowed: map[string]int{http.MethodGet: 200, http.MethodHead: 200}},
		{name: "audition", path: midiAuditionPath, allow: "POST", allowed: map[string]int{http.MethodPost: 202}},
		{name: "panic", path: midiPanicPath, allow: "POST", allowed: map[string]int{http.MethodPost: 204}},
	}
	for _, route := range routes {
		for _, method := range methods {
			t.Run(route.name+"/"+method, func(t *testing.T) {
				body := ""
				if route.name == "audition" && method == http.MethodPost {
					body = `{"channel":1,"note":60,"velocity":100,"duration_ms":100}`
				}
				request := localRequestFor(method, route.path, body)
				if body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				response := serve(mustHandler(t, &stubBackend{}), request)
				want, allowed := route.allowed[method]
				if !allowed {
					want = http.StatusMethodNotAllowed
				}
				assertStatus(t, response, want)
				wantAllow := ""
				if !allowed {
					wantAllow = route.allow
				}
				if got := response.Header().Get("Allow"); got != wantAllow {
					t.Fatalf("Allow = %q, want %q", got, wantAllow)
				}
			})
		}
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "devices query", method: http.MethodGet, path: midiDevicesPath + "?refresh=1"},
		{name: "audition query", method: http.MethodPost, path: midiAuditionPath + "?channel=1"},
		{name: "panic query", method: http.MethodPost, path: midiPanicPath + "?now=1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(test.method, test.path, "")
			assertProblem(t, serve(mustHandler(t, &stubBackend{}), request), http.StatusBadRequest, "invalid_query")
		})
	}

	for _, path := range []string{
		"/api/v1/midi",
		midiDevicesPath + "/",
		midiAuditionPath + "/",
		midiPanicPath + "/",
		"/api/v1/midi/reconnect",
	} {
		t.Run(path, func(t *testing.T) {
			assertProblem(t, serve(mustHandler(t, &stubBackend{}), localRequestFor(http.MethodGet, path, "")), http.StatusNotFound, "not_found")
		})
	}
}

func TestMIDIBackendErrors(t *testing.T) {
	private := errors.New("native helper path and private detail")
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		backend *stubBackend
		status  int
		code    string
	}{
		{name: "devices unavailable", method: http.MethodGet, path: midiDevicesPath, backend: &stubBackend{midiDevicesFunc: func(context.Context) (MIDIDevicesDocument, error) {
			return MIDIDevicesDocument{}, &BackendError{Kind: ErrorUnavailable, Err: private}
		}}, status: 503, code: "unavailable"},
		{name: "devices generic", method: http.MethodGet, path: midiDevicesPath, backend: &stubBackend{midiDevicesFunc: func(context.Context) (MIDIDevicesDocument, error) { return MIDIDevicesDocument{}, private }}, status: 500, code: "internal_error"},
		{name: "audition disabled", method: http.MethodPost, path: midiAuditionPath, body: `{"channel":1,"note":60,"velocity":100,"duration_ms":100}`, backend: &stubBackend{auditionMIDIFunc: func(context.Context, MIDIAuditionRequest) error {
			return &BackendError{Kind: ErrorConflict, Code: "midi_disabled", Detail: "MIDI output is disabled"}
		}}, status: 409, code: "midi_disabled"},
		{name: "audition rate", method: http.MethodPost, path: midiAuditionPath, body: `{"channel":1,"note":60,"velocity":100,"duration_ms":100}`, backend: &stubBackend{auditionMIDIFunc: func(context.Context, MIDIAuditionRequest) error {
			return &BackendError{Kind: ErrorRateLimited, Code: "midi_rate_limited", Detail: "MIDI audition was limited"}
		}}, status: 429, code: "midi_rate_limited"},
		{name: "audition unavailable", method: http.MethodPost, path: midiAuditionPath, body: `{"channel":1,"note":60,"velocity":100,"duration_ms":100}`, backend: &stubBackend{auditionMIDIFunc: func(context.Context, MIDIAuditionRequest) error {
			return &BackendError{Kind: ErrorUnavailable, Err: private}
		}}, status: 503, code: "unavailable"},
		{name: "panic disabled", method: http.MethodPost, path: midiPanicPath, backend: &stubBackend{panicMIDIFunc: func(context.Context) error {
			return &BackendError{Kind: ErrorConflict, Code: "midi_disabled", Detail: "MIDI output is disabled"}
		}}, status: 409, code: "midi_disabled"},
		{name: "panic failure", method: http.MethodPost, path: midiPanicPath, backend: &stubBackend{panicMIDIFunc: func(context.Context) error { return &BackendError{Kind: ErrorUnavailable, Err: private} }}, status: 503, code: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequestFor(test.method, test.path, test.body)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := serve(mustHandler(t, test.backend), request)
			assertProblem(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "native helper") || strings.Contains(response.Body.String(), "private detail") {
				t.Fatalf("response leaked backend detail: %s", response.Body.String())
			}
		})
	}
}

func assertMIDIDevicesDocument(t *testing.T, contents []byte, want MIDIDevicesDocument) {
	t.Helper()
	var got MIDIDevicesDocument
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("decode MIDI devices document: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MIDI devices document = %#v, want %#v", got, want)
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
