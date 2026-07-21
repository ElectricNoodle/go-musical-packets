package managementapi

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

func TestManagementInstrumentationNormalizesRequestLabels(t *testing.T) {
	observer := &recordingManagementObserver{}
	handler, err := NewHandler(&stubBackend{}, Options{AllowedPort: 8080, Observer: observer})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	requests := []*http.Request{
		localRequestFor(http.MethodGet, "/api/v1/status", ""),
		localRequestFor(http.MethodGet, rulesCollectionPath+"/arbitrary-user-rule-id", ""),
		localRequestFor(http.MethodTrace, "/api/v1/not-a-route", ""),
	}
	for _, request := range requests {
		serve(handler, request)
	}

	want := []observedManagementRequest{
		{route: "/api/v1/status", method: http.MethodGet, result: "success"},
		{route: rulesCollectionPath + "/{id}", method: http.MethodGet, result: "client_error"},
		{route: unknownManagementRoute, method: "OTHER", result: "client_error"},
	}
	if got := observer.requestSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("observed requests = %#v, want %#v", got, want)
	}
}

func TestManagementInstrumentationClassifiesConfigUpdates(t *testing.T) {
	observer := &recordingManagementObserver{}
	handler, err := NewHandler(&stubBackend{}, Options{AllowedPort: 8080, Observer: observer})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	contents, err := config.Encode(config.Default())
	if err != nil {
		t.Fatalf("encode default config: %v", err)
	}

	success := localRequestFor(http.MethodPut, "/api/v1/config", string(contents))
	success.Header.Set("Content-Type", "application/yaml")
	success.Header.Set("If-Match", `"`+testRevisionA+`"`)
	assertStatus(t, serve(handler, success), http.StatusOK)

	missingPrecondition := localRequestFor(http.MethodPut, "/api/v1/config", string(contents))
	missingPrecondition.Header.Set("Content-Type", "application/yaml")
	assertStatus(t, serve(handler, missingPrecondition), http.StatusPreconditionRequired)

	pending := localRequestFor(http.MethodPut, "/api/v1/config/pending", string(contents))
	pending.Header.Set("Content-Type", "application/yaml")
	pending.Header.Set("If-Match", `"`+testRevisionA+`"`)
	assertStatus(t, serve(handler, pending), http.StatusOK)

	discard := localRequestFor(http.MethodDelete, "/api/v1/config/pending", "")
	discard.Header.Set("If-Match", `"`+testRevisionB+`"`)
	assertStatus(t, serve(handler, discard), http.StatusOK)

	if got, want := observer.updateSnapshot(), []string{"success", "precondition", "success", "success"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("config update results = %#v, want %#v", got, want)
	}
}

type observedManagementRequest struct {
	route  string
	method string
	result string
}

type recordingManagementObserver struct {
	mu       sync.Mutex
	requests []observedManagementRequest
	updates  []string
}

func (observer *recordingManagementObserver) Request(route, method, result string, _ time.Duration) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.requests = append(observer.requests, observedManagementRequest{route: route, method: method, result: result})
}

func (observer *recordingManagementObserver) ConfigUpdate(result string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.updates = append(observer.updates, result)
}

func (observer *recordingManagementObserver) requestSnapshot() []observedManagementRequest {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]observedManagementRequest(nil), observer.requests...)
}

func (observer *recordingManagementObserver) updateSnapshot() []string {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]string(nil), observer.updates...)
}
