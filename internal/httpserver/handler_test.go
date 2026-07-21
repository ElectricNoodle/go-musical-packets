package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHandlerServesMetricsAndChecks(t *testing.T) {
	registry := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_packets_total", Help: "Test packets."})
	registry.MustRegister(counter)
	counter.Add(2)

	handler, err := NewHandler(
		registry,
		func(context.Context) error { return nil },
		func(context.Context) error { return errors.New("MIDI output unavailable") },
	)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	tests := []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{path: "/metrics", wantStatus: http.StatusOK, wantBody: "test_packets_total 2"},
		{path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok\n"},
		{path: "/readyz", wantStatus: http.StatusServiceUnavailable, wantBody: "not ready: MIDI output unavailable"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", response.Body.String(), test.wantBody)
			}
		})
	}
}

func TestHandlerDefaultsChecksToHealthy(t *testing.T) {
	handler, err := NewHandler(prometheus.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, response.Code)
		}
	}
}

func TestHandlerRejectsMissingGatherer(t *testing.T) {
	if _, err := NewHandler(nil, nil, nil); err == nil {
		t.Fatal("NewHandler(nil) error = nil")
	}
}
