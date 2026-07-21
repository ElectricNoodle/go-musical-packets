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

func TestInterfacesGetAndHead(t *testing.T) {
	want := InterfacesDocument{
		Configured: "auto",
		Selected:   "en0",
		Interfaces: []CaptureInterface{
			{Name: "en0", Description: "Ethernet", Addresses: []string{"192.0.2.10/24"}, Up: true},
			{Name: "lo0", Addresses: []string{"127.0.0.1/8", "::1/128"}, Up: true, Loopback: true},
		},
	}
	calls := 0
	handler := mustHandler(t, &stubBackend{interfacesFunc: func(ctx context.Context) (InterfacesDocument, error) {
		calls++
		if ctx == nil {
			t.Fatal("Interfaces context = nil")
		}
		return want, nil
	}})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := serve(handler, localRequestFor(method, interfacesPath, ""))
			assertStatus(t, response, http.StatusOK)
			assertSecurityHeaders(t, response)
			if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Content-Length") == "" {
				t.Fatalf("interface response headers = %v", response.Header())
			}
			if method == http.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q, want empty", response.Body.String())
				}
				return
			}
			var got InterfacesDocument
			decodeJSONBody(t, response.Body.Bytes(), &got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("interfaces document = %#v, want %#v", got, want)
			}
		})
	}
	if calls != 2 {
		t.Fatalf("Interfaces calls = %d, want 2", calls)
	}
}

func TestInterfacesRejectsInvalidBackendDocuments(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tests := []InterfacesDocument{
		{},
		{Configured: "auto"},
		{Configured: invalidUTF8, Interfaces: []CaptureInterface{}},
		{Configured: "auto", Selected: "missing", Interfaces: []CaptureInterface{}},
		{Configured: "auto", Interfaces: []CaptureInterface{{Name: ""}}},
		{Configured: "auto", Interfaces: []CaptureInterface{{Name: invalidUTF8, Addresses: []string{}}}},
		{Configured: "auto", Interfaces: []CaptureInterface{{Name: "en0"}, {Name: "en0"}}},
		{Configured: "auto", Interfaces: []CaptureInterface{{Name: "en0", Addresses: []string{"not-a-prefix"}}}},
		{Configured: "auto", Interfaces: []CaptureInterface{{Name: "en0", Addresses: []string{"192.0.2.0/24", "192.0.2.0/24"}}}},
	}
	for index, document := range tests {
		t.Run(boolText(index != 0)+"/"+document.Configured, func(t *testing.T) {
			handler := mustHandler(t, &stubBackend{interfacesFunc: func(context.Context) (InterfacesDocument, error) {
				return document, nil
			}})
			assertProblem(t, serve(handler, localRequestFor(http.MethodGet, interfacesPath, "")), http.StatusInternalServerError, "internal_error")
		})
	}
}

func TestInterfacesMethodQueryAndBackendErrors(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		response := serve(mustHandler(t, &stubBackend{}), localRequestFor(method, interfacesPath, ""))
		assertProblem(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
		if response.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("Allow = %q, want GET, HEAD", response.Header().Get("Allow"))
		}
	}
	assertProblem(t, serve(mustHandler(t, &stubBackend{}), localRequestFor(http.MethodGet, interfacesPath+"?refresh=1", "")), http.StatusBadRequest, "invalid_query")
	assertProblem(t, serve(mustHandler(t, &stubBackend{}), localRequestFor(http.MethodGet, interfacesPath+"/", "")), http.StatusNotFound, "not_found")

	private := "private native discovery detail"
	handler := mustHandler(t, &stubBackend{interfacesFunc: func(context.Context) (InterfacesDocument, error) {
		return InterfacesDocument{}, &BackendError{Kind: ErrorUnavailable, Err: errors.New(private)}
	}})
	response := serve(handler, localRequestFor(http.MethodGet, interfacesPath, ""))
	assertProblem(t, response, http.StatusServiceUnavailable, "unavailable")
	if strings.Contains(response.Body.String(), private) {
		t.Fatalf("response leaked backend detail: %s", response.Body.String())
	}
}

func decodeJSONBody(t *testing.T, contents []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(contents, target); err != nil {
		t.Fatalf("decode JSON response: %v; body=%s", err, contents)
	}
}
