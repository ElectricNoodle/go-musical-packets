// Package httpserver provides the application's management HTTP surface.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check reports whether one operational condition is currently satisfied.
type Check func(context.Context) error

// NewHandler constructs the base management routes. Additional API and UI
// routes can be mounted around this handler as later milestones are composed.
func NewHandler(gatherer prometheus.Gatherer, health, readiness Check) (http.Handler, error) {
	if gatherer == nil {
		return nil, errors.New("Prometheus gatherer is required")
	}
	if health == nil {
		health = func(context.Context) error { return nil }
	}
	if readiness == nil {
		readiness = func(context.Context) error { return nil }
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", checkHandler("unhealthy", health))
	mux.HandleFunc("GET /readyz", checkHandler("not ready", readiness))
	return mux, nil
}

func checkHandler(failure string, check Check) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		if err := check(request.Context()); err != nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(response, "%s: %v\n", failure, err)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	}
}
