package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
)

func TestManagementBackendRedactsAndResolvesSecrets(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())

	document, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	assertManagementConfigRedacted(t, document.Config, configuration)
	rawInitial := controller.Current().Revision
	if document.Revision == "" || document.Revision.String() == rawInitial.String() {
		t.Fatalf("public revision = %q, want non-empty token distinct from raw digest %q", document.Revision, rawInitial)
	}
	repeated, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("second Config() error = %v", err)
	}
	if repeated.Revision != document.Revision {
		t.Fatalf("stable config revision = %q, want %q", repeated.Revision, document.Revision)
	}
	status, err := backend.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Revision != document.Revision {
		t.Fatalf("status revision = %q, want stable config token %q", status.Revision, document.Revision)
	}

	// The management representation is intentionally round-trippable: the
	// placeholders preserve the active secrets while ordinary hot fields can
	// change.
	candidate := document.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay
	updated, err := backend.UpdateConfig(context.Background(), document.Revision, candidate)
	if err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	assertManagementConfigRedacted(t, updated.Config, configuration)
	if updated.Config.Mapping.DefaultState != config.FlowPlay {
		t.Fatalf("UpdateConfig() default state = %q, want %q", updated.Config.Mapping.DefaultState, config.FlowPlay)
	}
	if updated.Revision == document.Revision {
		t.Fatalf("updated revision = %q, want a new public token", updated.Revision)
	}
	persisted, err := repository.Read()
	if err != nil {
		t.Fatalf("repository Read() error = %v", err)
	}
	if persisted.Config.Mapping.Seed != configuration.Mapping.Seed {
		t.Fatalf("persisted mapping seed = %q, want original secret", persisted.Config.Mapping.Seed)
	}
	if persisted.Config.Peer.URL != configuration.Peer.URL {
		t.Fatalf("persisted peer URL = %q, want original secret", persisted.Config.Peer.URL)
	}
	if persisted.Config.Mapping.DefaultState != config.FlowPlay {
		t.Fatalf("persisted default state = %q, want %q", persisted.Config.Mapping.DefaultState, config.FlowPlay)
	}
	if updated.Revision.String() == persisted.Revision.String() {
		t.Fatalf("updated public revision = raw repository digest %q", persisted.Revision)
	}
	afterUpdate, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() after update error = %v", err)
	}
	if afterUpdate.Revision != updated.Revision {
		t.Fatalf("Config() revision after update = %q, want stable %q", afterUpdate.Revision, updated.Revision)
	}
}

func TestManagementBackendMapsUpdateFailuresAndRejectsWritesWhenUnavailable(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	current := controller.Current()
	currentPublic := backend.revisions.issue(current.Revision)

	staleRevision := managementapi.Revision(strings.Repeat("f", 64))
	_, err := backend.UpdateConfig(context.Background(), staleRevision, current.Config.Redacted())
	stale := assertManagementBackendError(t, err, managementapi.ErrorPreconditionFailed, "revision_conflict")
	if stale.ActualRevision != currentPublic {
		t.Fatalf("stale actual revision = %q, want public token %q", stale.ActualRevision, currentPublic)
	}
	if stale.ActualRevision.String() == current.Revision.String() {
		t.Fatalf("stale response exposed raw repository digest %q", current.Revision)
	}

	restartCandidate := current.Config.Redacted()
	restartCandidate.Server.ReadTimeout += time.Second
	_, err = backend.UpdateConfig(context.Background(), currentPublic, restartCandidate)
	restart := assertManagementBackendError(t, err, managementapi.ErrorConflict, "restart_required")
	if len(restart.Fields) != 1 || restart.Fields[0] != "server.read_timeout" {
		t.Fatalf("restart fields = %v, want [server.read_timeout]", restart.Fields)
	}

	ready.Store(false)
	hotCandidate := current.Config.Redacted()
	hotCandidate.Mapping.DefaultChannel = 2
	_, err = backend.UpdateConfig(context.Background(), currentPublic, hotCandidate)
	assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")
	status, err := backend.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != "unavailable" || status.Warning != "runtime is starting or stopping" {
		t.Fatalf("Status() = %#v, want unavailable runtime", status)
	}

	persisted, err := repository.Read()
	if err != nil {
		t.Fatalf("repository Read() error = %v", err)
	}
	if persisted.Revision != current.Revision || persisted.Config.Mapping.DefaultChannel != current.Config.Mapping.DefaultChannel {
		t.Fatalf("rejected writes changed repository: %#v", persisted)
	}
}

func TestManagementBackendStatusSanitizesControllerWarnings(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, newMemoryConfigRepository(configuration), nil)
	controller.store.publish(snapshotWithStatus(
		controller.store.current.Load(),
		ControllerStateOutOfSync,
		"open /private/config.yaml: permission denied",
	))
	var ready atomic.Bool
	ready.Store(true)
	status, err := newTestManagementBackend(controller, &ready, context.Background()).Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != string(ControllerStateOutOfSync) || status.Warning != "active and durable configuration are out of sync" {
		t.Fatalf("Status() = %#v, want sanitized out-of-sync warning", status)
	}
	if strings.Contains(status.Warning, "/private/") {
		t.Fatalf("Status() leaked controller detail %q", status.Warning)
	}
}

func TestManagementBackendCancelsActiveUpdateWhenRuntimeStops(t *testing.T) {
	configuration := managementTestConfig()
	repository := &cancelAwareConfigRepository{
		snapshot: memorySnapshot(configuration),
		started:  make(chan struct{}),
	}
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	lifecycle, stopRuntime := context.WithCancel(context.Background())
	backend := newTestManagementBackend(controller, &ready, lifecycle)
	candidate := configuration.Redacted()
	candidate.Mapping.DefaultState = config.FlowPlay
	expected := backend.revisions.issue(repository.snapshot.Revision)

	done := make(chan error, 1)
	go func() {
		_, err := backend.UpdateConfig(context.Background(), expected, candidate)
		done <- err
	}()
	select {
	case <-repository.started:
	case <-time.After(time.Second):
		t.Fatal("UpdateConfig() did not reach repository mutation")
	}
	ready.Store(false)
	stopRuntime()
	select {
	case err := <-done:
		assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "update_unavailable")
	case <-time.After(time.Second):
		t.Fatal("UpdateConfig() did not stop after runtime cancellation")
	}
	if repository.snapshot.Config.Mapping.DefaultState != configuration.Mapping.DefaultState {
		t.Fatal("canceled runtime update changed durable configuration")
	}
}

func TestManagementBackendConcurrentSameRevisionUpdatesHaveOneWinner(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	current := controller.Current()
	expected := backend.revisions.issue(current.Revision)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, channel := range []uint8{2, 3} {
		candidate := current.Config.Redacted()
		candidate.Mapping.DefaultChannel = channel
		go func(candidate config.Config) {
			<-start
			_, err := backend.UpdateConfig(context.Background(), expected, candidate)
			results <- err
		}(candidate)
	}
	close(start)

	var successes, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var backendError *managementapi.BackendError
		if errors.As(err, &backendError) && backendError.Kind == managementapi.ErrorPreconditionFailed {
			conflicts++
			continue
		}
		t.Fatalf("concurrent update error = %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = %d successes, %d conflicts; want one each", successes, conflicts)
	}
}

func TestManagementBackendOutOfSyncConflictReturnsDurableTokenForRetry(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	active, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	handler := newTestManagementHandler(t, backend)

	external := configuration.Clone()
	external.Mapping.DefaultChannel = 4
	durableRaw := repository.externalReplace(external)
	candidate := active.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay

	conflict := serveTestManagementRequest(t, handler, http.MethodPut, "/api/v1/config", encodeManagementConfig(t, candidate), map[string]string{
		"Content-Type": "application/yaml",
		"If-Match":     `"` + active.Revision.String() + `"`,
	})
	assertManagementProblem(t, conflict.Code, conflict.Body.Bytes(), http.StatusPreconditionFailed, "revision_conflict", nil)
	durableETag := conflict.Header().Get("ETag")
	if len(durableETag) != 66 || durableETag[0] != '"' || durableETag[len(durableETag)-1] != '"' {
		t.Fatalf("out-of-sync 412 ETag = %q, want strong public revision", durableETag)
	}
	durablePublic := managementapi.Revision(durableETag[1 : len(durableETag)-1])
	if durablePublic == "" || durablePublic == active.Revision {
		t.Fatalf("durable conflict token = %q, want non-empty token distinct from active %q", durablePublic, active.Revision)
	}
	if durablePublic.String() == durableRaw.String() {
		t.Fatalf("durable conflict token exposed raw repository digest %q", durableRaw)
	}
	if want := backend.revisions.issue(durableRaw); durablePublic != want {
		t.Fatalf("durable conflict token = %q, want stable %q", durablePublic, want)
	}
	if got := controller.Current().State; got != ControllerStateOutOfSync {
		t.Fatalf("controller state after drift = %q, want %q", got, ControllerStateOutOfSync)
	}

	retry := serveTestManagementRequest(t, handler, http.MethodPut, "/api/v1/config", encodeManagementConfig(t, candidate), map[string]string{
		"Content-Type": "application/yaml",
		"If-Match":     durableETag,
	})
	if retry.Code != http.StatusOK {
		t.Fatalf("PUT reconciliation retry = %d, %q; want 200", retry.Code, retry.Body.Bytes())
	}
	reconciledETag := retry.Header().Get("ETag")
	if reconciledETag == "" || reconciledETag == durableETag || reconciledETag == `"`+controller.Current().Revision.String()+`"` {
		t.Fatalf("reconciled ETag = %q, want new token distinct from durable/raw revisions", reconciledETag)
	}
	if got := controller.Current(); got.State != ControllerStateReady || got.Config.Mapping.DefaultState != config.FlowPlay {
		t.Fatalf("reconciled controller = %#v, want ready play configuration", got)
	}
	persisted, err := repository.Read()
	if err != nil {
		t.Fatalf("repository Read() error = %v", err)
	}
	if persisted.Revision != controller.Current().Revision || persisted.Config.Mapping.DefaultState != config.FlowPlay {
		t.Fatalf("reconciled repository = %#v, want active play configuration", persisted)
	}
	status, err := backend.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != string(ControllerStateReady) || `"`+status.Revision.String()+`"` != reconciledETag {
		t.Fatalf("status after reconciliation = %#v, want ready revision ETag %q", status, reconciledETag)
	}
}

func TestManagementBackendOversizeResolvedSecretUsesGenericError(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Mapping.Seed = strings.Repeat("s", config.MaximumBytes)
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	document, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	candidate := document.Config.Clone()
	candidate.Mapping.DefaultState = config.FlowPlay
	encodedCandidate := encodeManagementConfig(t, candidate)
	if len(encodedCandidate) > config.MaximumBytes {
		t.Fatalf("redacted candidate size = %d, want transport-admissible request", len(encodedCandidate))
	}
	resolved, err := config.ResolveRedacted(candidate, configuration)
	if err != nil {
		t.Fatalf("ResolveRedacted() error = %v", err)
	}
	encodedResolved := encodeManagementConfig(t, resolved)
	if len(encodedResolved) <= config.MaximumBytes {
		t.Fatalf("resolved candidate size = %d, want over %d", len(encodedResolved), config.MaximumBytes)
	}
	handler := newTestManagementHandler(t, backend)

	assertGeneric := func(t *testing.T, response *httptest.ResponseRecorder) {
		t.Helper()
		const generic = "canonical configuration exceeds the maximum size"
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatalf("decode oversize problem %q: %v", response.Body.Bytes(), err)
		}
		if response.Code != http.StatusUnprocessableEntity || problem.Code != "invalid_config" || problem.Detail != generic {
			t.Fatalf("oversize response = HTTP %d %#v, want 422 invalid_config %q", response.Code, problem, generic)
		}
		for _, secretLength := range []int{len(configuration.Mapping.Seed), len(encodedResolved)} {
			if strings.Contains(problem.Detail, strconv.Itoa(secretLength)) {
				t.Fatalf("oversize error %q exposed resolved length %d", problem.Detail, secretLength)
			}
		}
	}

	validate := serveTestManagementRequest(t, handler, http.MethodPost, "/api/v1/config/validate", encodedCandidate, map[string]string{
		"Content-Type": "application/yaml",
	})
	assertGeneric(t, validate)
	update := serveTestManagementRequest(t, handler, http.MethodPut, "/api/v1/config", encodedCandidate, map[string]string{
		"Content-Type": "application/yaml",
		"If-Match":     `"` + document.Revision.String() + `"`,
	})
	assertGeneric(t, update)
	if got := controller.Current(); got.Revision.String() == "" || got.Config.Mapping.DefaultState != configuration.Mapping.DefaultState {
		t.Fatalf("oversize request changed controller: revision %q state %q", got.Revision, got.Config.Mapping.DefaultState)
	}
}

func TestManagementAPIRejectsConcreteSecretGuessesIdentically(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, newMemoryConfigRepository(configuration), nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	document, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	handler := newTestManagementHandler(t, backend)

	tests := []struct {
		name    string
		guesses []string
		set     func(*config.Config, string)
	}{
		{
			name:    "mapping seed",
			guesses: []string{strings.Repeat("x", len(configuration.Mapping.Seed)), configuration.Mapping.Seed},
			set:     func(candidate *config.Config, guess string) { candidate.Mapping.Seed = guess },
		},
		{
			name:    "peer URL",
			guesses: []string{"wss://wrong.example.test/socket?token=not-the-secret", configuration.Peer.URL},
			set:     func(candidate *config.Config, guess string) { candidate.Peer.URL = guess },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, endpoint := range []struct {
				method  string
				path    string
				headers map[string]string
			}{
				{method: http.MethodPost, path: "/api/v1/config/validate", headers: map[string]string{"Content-Type": "application/yaml"}},
				{method: http.MethodPut, path: "/api/v1/config", headers: map[string]string{
					"Content-Type": "application/yaml",
					"If-Match":     `"` + document.Revision.String() + `"`,
				}},
			} {
				var firstBody []byte
				for _, guess := range test.guesses {
					candidate := document.Config.Clone()
					test.set(&candidate, guess)
					response := serveTestManagementRequest(t, handler, endpoint.method, endpoint.path, encodeManagementConfig(t, candidate), endpoint.headers)
					assertManagementProblem(t, response.Code, response.Body.Bytes(), http.StatusUnprocessableEntity, "invalid_config", nil)
					if firstBody == nil {
						firstBody = bytes.Clone(response.Body.Bytes())
					} else if !bytes.Equal(response.Body.Bytes(), firstBody) {
						t.Fatalf("%s responses distinguish concrete guesses: %q and %q", endpoint.path, firstBody, response.Body.Bytes())
					}
				}
			}
		})
	}
}

func TestRunMountsManagementAPIOnlyOnLoopbackListener(t *testing.T) {
	t.Run("loopback", func(t *testing.T) {
		configuration := managementTestConfig()
		repository := newMemoryConfigRepository(configuration)
		listenerReady := make(chan net.Listener, 1)
		listener := newAddressedMemoryListener(&net.TCPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 43131,
		})
		dependencies := Dependencies{
			OpenConfigRepository: func(string) (ConfigRepository, error) { return repository, nil },
			Listen: func(string, string) (net.Listener, error) {
				tracked := &trackingListener{Listener: listener}
				listenerReady <- tracked
				return tracked, nil
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- RunWithOptionsAndDependencies(
				ctx,
				config.Config{},
				RunOptions{ConfigPath: "/test/config.yaml"},
				dependencies,
			)
		}()
		bound := awaitListener(t, listenerReady, done)
		client := listenerHTTPClient(t, bound)
		defer client.CloseIdleConnections()
		awaitManagementReady(t, client, bound)

		statusResponse := managementRequest(t, client, bound, http.MethodGet, "/api/v1/status", nil, nil)
		defer statusResponse.Body.Close()
		statusBody := readManagementBody(t, statusResponse)
		if statusResponse.StatusCode != http.StatusOK {
			t.Fatalf("GET status = %d, %q; want 200", statusResponse.StatusCode, statusBody)
		}
		var status managementapi.Status
		if err := json.Unmarshal(statusBody, &status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		initial := memorySnapshot(configuration)
		if status.State != string(ControllerStateReady) || !status.Writable || status.Revision == "" {
			t.Fatalf("GET status = %#v, want ready writable public revision", status)
		}
		if status.Revision.String() == initial.Revision.String() {
			t.Fatalf("GET status exposed raw repository digest %q", initial.Revision)
		}

		getResponse := managementRequest(t, client, bound, http.MethodGet, "/api/v1/config", nil, nil)
		getBody := readManagementBody(t, getResponse)
		_ = getResponse.Body.Close()
		if getResponse.StatusCode != http.StatusOK {
			t.Fatalf("GET config = %d, %q; want 200", getResponse.StatusCode, getBody)
		}
		initialETag := `"` + status.Revision.String() + `"`
		if got := getResponse.Header.Get("ETag"); got != initialETag {
			t.Fatalf("GET config ETag = %q, want stable status token %q", got, initialETag)
		}
		redacted := decodeManagementConfig(t, getBody)
		assertManagementConfigRedacted(t, redacted, configuration)
		assertSecretsAbsent(t, getBody, configuration)

		redacted.Mapping.DefaultState = config.FlowPlay
		putBody := encodeManagementConfig(t, redacted)
		repository.warning = errors.New("sync /private/config-directory: injected uncertainty")
		putResponse := managementRequest(t, client, bound, http.MethodPut, "/api/v1/config", putBody, map[string]string{
			"Content-Type": "application/yaml",
			"If-Match":     initialETag,
		})
		updatedBody := readManagementBody(t, putResponse)
		_ = putResponse.Body.Close()
		if putResponse.StatusCode != http.StatusOK {
			t.Fatalf("PUT config = %d, %q; want 200", putResponse.StatusCode, updatedBody)
		}
		updatedETag := putResponse.Header.Get("ETag")
		if updatedETag == "" || updatedETag == initialETag {
			t.Fatalf("PUT config ETag = %q, want new strong ETag", updatedETag)
		}
		updatedRedacted := decodeManagementConfig(t, updatedBody)
		assertManagementConfigRedacted(t, updatedRedacted, configuration)
		assertSecretsAbsent(t, updatedBody, configuration)
		if updatedRedacted.Mapping.DefaultState != config.FlowPlay {
			t.Fatalf("PUT response default state = %q, want %q", updatedRedacted.Mapping.DefaultState, config.FlowPlay)
		}

		persisted, err := repository.Read()
		if err != nil {
			t.Fatalf("repository Read() error = %v", err)
		}
		if persisted.Config.Mapping.DefaultState != config.FlowPlay ||
			persisted.Config.Mapping.Seed != configuration.Mapping.Seed ||
			persisted.Config.Peer.URL != configuration.Peer.URL {
			t.Fatalf("persisted hot update did not preserve secrets: %#v", persisted.Config)
		}
		if updatedETag == `"`+persisted.Revision.String()+`"` {
			t.Fatalf("PUT ETag exposed raw persisted revision %q", persisted.Revision)
		}
		getUpdatedResponse := managementRequest(t, client, bound, http.MethodGet, "/api/v1/config", nil, nil)
		getUpdatedBody := readManagementBody(t, getUpdatedResponse)
		_ = getUpdatedResponse.Body.Close()
		if getUpdatedResponse.StatusCode != http.StatusOK || getUpdatedResponse.Header.Get("ETag") != updatedETag {
			t.Fatalf("GET updated config = %d ETag %q body %q; want 200 stable ETag %q", getUpdatedResponse.StatusCode, getUpdatedResponse.Header.Get("ETag"), getUpdatedBody, updatedETag)
		}
		readyResponse := managementRequest(t, client, bound, http.MethodGet, "/readyz", nil, nil)
		readyBody := readManagementBody(t, readyResponse)
		_ = readyResponse.Body.Close()
		if readyResponse.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(readyBody, []byte("durability_uncertain")) {
			t.Fatalf("GET readiness after durability warning = %d, %q", readyResponse.StatusCode, readyBody)
		}
		warningResponse := managementRequest(t, client, bound, http.MethodGet, "/api/v1/status", nil, nil)
		warningBody := readManagementBody(t, warningResponse)
		_ = warningResponse.Body.Close()
		if warningResponse.StatusCode != http.StatusOK || bytes.Contains(warningBody, []byte("/private/")) || !bytes.Contains(warningBody, []byte(`"state":"durability_uncertain"`)) {
			t.Fatalf("GET status after durability warning = %d, %q", warningResponse.StatusCode, warningBody)
		}

		staleCandidate := updatedRedacted.Clone()
		staleCandidate.Mapping.DefaultChannel = 2
		staleResponse := managementRequest(t, client, bound, http.MethodPut, "/api/v1/config", encodeManagementConfig(t, staleCandidate), map[string]string{
			"Content-Type": "application/yaml",
			"If-Match":     initialETag,
		})
		staleBody := readManagementBody(t, staleResponse)
		_ = staleResponse.Body.Close()
		assertManagementProblem(t, staleResponse.StatusCode, staleBody, http.StatusPreconditionFailed, "revision_conflict", nil)
		if got := staleResponse.Header.Get("ETag"); got != updatedETag {
			t.Fatalf("stale PUT ETag = %q, want current %q", got, updatedETag)
		}

		restartCandidate := updatedRedacted.Clone()
		restartCandidate.Server.ReadTimeout += time.Second
		restartResponse := managementRequest(t, client, bound, http.MethodPut, "/api/v1/config", encodeManagementConfig(t, restartCandidate), map[string]string{
			"Content-Type": "application/yaml",
			"If-Match":     updatedETag,
		})
		restartBody := readManagementBody(t, restartResponse)
		_ = restartResponse.Body.Close()
		assertManagementProblem(t, restartResponse.StatusCode, restartBody, http.StatusConflict, "restart_required", []string{"server.read_timeout"})

		afterRejected, err := repository.Read()
		if err != nil {
			t.Fatalf("repository Read() after rejected updates error = %v", err)
		}
		if afterRejected.Revision != persisted.Revision || afterRejected.Config.Server.ReadTimeout != persisted.Config.Server.ReadTimeout {
			t.Fatalf("rejected updates changed repository: %#v", afterRejected)
		}

		cancel()
		if err := await(t, done); err != nil {
			t.Fatalf("RunWithOptionsAndDependencies() error = %v, want nil", err)
		}
	})

	t.Run("non-loopback", func(t *testing.T) {
		configuration := testConfig()
		configuration.Capture.Enabled = false
		configuration.MIDI.Enabled = false
		listenerReady := make(chan net.Listener, 1)
		listener := newAddressedMemoryListener(&net.TCPAddr{
			IP:   net.ParseIP("192.0.2.25"),
			Port: 43132,
		})
		dependencies := Dependencies{Listen: func(string, string) (net.Listener, error) {
			tracked := &trackingListener{Listener: listener}
			listenerReady <- tracked
			return tracked, nil
		}}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
		bound := awaitListener(t, listenerReady, done)
		client := listenerHTTPClient(t, bound)
		defer client.CloseIdleConnections()
		eventuallyHTTP(t, bound, "/healthz", http.StatusOK, "ok\n")

		response := managementRequest(t, client, bound, http.MethodGet, "/api/v1/status", nil, nil)
		body := readManagementBody(t, response)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET non-loopback management route = %d, %q; want 404", response.StatusCode, body)
		}

		cancel()
		if err := await(t, done); err != nil {
			t.Fatalf("RunWithDependencies() error = %v, want nil", err)
		}
	})
}

func TestRunWithOptionsRejectsExpectedRevisionDriftBeforeNativeBoundaries(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	actual := memorySnapshot(configuration).Revision
	expected := config.Revision(strings.Repeat("e", 64))
	var nativeCalls atomic.Int32
	called := func() { nativeCalls.Add(1) }
	dependencies := Dependencies{
		OpenConfigRepository: func(string) (ConfigRepository, error) { return repository, nil },
		Interfaces: func() ([]capture.Interface, error) {
			called()
			return nil, nil
		},
		OpenLive: func(config capture.LiveConfig) (capture.Source, error) {
			called()
			return nil, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) {
			called()
			return nil, nil
		},
		Listen: func(string, string) (net.Listener, error) {
			called()
			return nil, errors.New("unexpected listener")
		},
	}

	err := RunWithOptionsAndDependencies(
		context.Background(),
		config.Config{},
		RunOptions{ConfigPath: "/test/config.yaml", ExpectedRevision: expected},
		dependencies,
	)
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("RunWithOptionsAndDependencies() error = %v, want *config.ConflictError", err)
	}
	if conflict.Expected != expected || conflict.Actual != actual {
		t.Fatalf("revision conflict = %#v, want expected %q actual %q", conflict, expected, actual)
	}
	if got := nativeCalls.Load(); got != 0 {
		t.Fatalf("native boundary calls = %d, want 0", got)
	}
}

func managementTestConfig() config.Config {
	configuration := testConfig()
	configuration.Capture.Enabled = false
	configuration.MIDI.Enabled = false
	configuration.Mapping.Seed = "management-test-secret-seed"
	configuration.Peer.URL = "wss://peer.example.test/socket?token=management-secret"
	return configuration
}

func newTestManagementBackend(controller *Controller, ready *atomic.Bool, lifecycle context.Context, registries ...*flow.Registry) *managementBackend {
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	if len(registries) > 1 {
		panic("newTestManagementBackend accepts at most one registry")
	}
	var registry *flow.Registry
	if len(registries) == 1 {
		registry = registries[0]
	} else {
		configuration := controller.Current().Config
		var err error
		registry, err = flow.NewRegistry(flow.RegistryConfig{
			Seed:     configuration.Mapping.Seed,
			Capacity: configuration.Performance.FlowRegistryCapacity,
			TTL:      configuration.Performance.FlowTTL,
		})
		if err != nil {
			panic(err)
		}
	}
	var key [sha256.Size]byte
	for index := range key {
		key[index] = byte(index + 1)
	}
	return &managementBackend{
		controller: controller,
		registry:   registry,
		ready:      ready,
		lifecycle:  lifecycle,
		revisions:  newManagementRevisionCodecWithKey(key),
	}
}

func newTestManagementHandler(t *testing.T, backend *managementBackend) http.Handler {
	t.Helper()
	handler, err := managementapi.NewHandler(backend, managementapi.Options{AllowedPort: 43133})
	if err != nil {
		t.Fatalf("managementapi.NewHandler() error = %v", err)
	}
	return handler
}

func serveTestManagementRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body []byte,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:43133"+path, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:53133"
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertManagementConfigRedacted(t *testing.T, got, original config.Config) {
	t.Helper()
	if got.Mapping.Seed != config.RedactedValue {
		t.Fatalf("management mapping seed = %q, want redaction placeholder", got.Mapping.Seed)
	}
	if got.Peer.URL != config.RedactedURLValue {
		t.Fatalf("management peer URL = %q, want redaction placeholder", got.Peer.URL)
	}
	if original.Mapping.Seed == config.RedactedValue || original.Peer.URL == config.RedactedURLValue {
		t.Fatal("test configuration does not contain concrete secrets")
	}
}

func assertSecretsAbsent(t *testing.T, body []byte, configuration config.Config) {
	t.Helper()
	for _, secret := range []string{configuration.Mapping.Seed, configuration.Peer.URL} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("management response exposed secret %q in %q", secret, body)
		}
	}
}

func assertManagementBackendError(t *testing.T, err error, kind managementapi.ErrorKind, code string) *managementapi.BackendError {
	t.Helper()
	var backendError *managementapi.BackendError
	if !errors.As(err, &backendError) {
		t.Fatalf("error = %v, want *managementapi.BackendError", err)
	}
	if backendError.Kind != kind || backendError.Code != code {
		t.Fatalf("backend error = %#v, want kind %q code %q", backendError, kind, code)
	}
	return backendError
}

func assertManagementProblem(t *testing.T, gotStatus int, body []byte, wantStatus int, wantCode string, wantFields []string) {
	t.Helper()
	var got struct {
		Status int      `json:"status"`
		Code   string   `json:"code"`
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode management problem %q: %v", body, err)
	}
	if gotStatus != wantStatus || got.Status != wantStatus || got.Code != wantCode {
		t.Fatalf("management problem = HTTP %d %#v, want HTTP/status %d code %q", gotStatus, got, wantStatus, wantCode)
	}
	if len(got.Fields) != len(wantFields) {
		t.Fatalf("management problem fields = %v, want %v", got.Fields, wantFields)
	}
	for index := range wantFields {
		if got.Fields[index] != wantFields[index] {
			t.Fatalf("management problem fields = %v, want %v", got.Fields, wantFields)
		}
	}
}

func awaitManagementReady(t *testing.T, client *http.Client, listener net.Listener) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response := managementRequest(t, client, listener, http.MethodGet, "/api/v1/status", nil, nil)
		body := readManagementBody(t, response)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK && bytes.Contains(body, []byte(`"state":"ready"`)) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("management API did not become ready")
}

func managementRequest(
	t *testing.T,
	client *http.Client,
	listener net.Listener,
	method string,
	path string,
	body []byte,
	headers map[string]string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+listener.Addr().String()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(%s %s) error = %v", method, path, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s error = %v", method, path, err)
	}
	return response
}

func readManagementBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read management response: %v", err)
	}
	return body
}

func decodeManagementConfig(t *testing.T, body []byte) config.Config {
	t.Helper()
	configuration, err := config.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode management config: %v", err)
	}
	return configuration
}

func encodeManagementConfig(t *testing.T, configuration config.Config) []byte {
	t.Helper()
	body, err := config.Encode(configuration)
	if err != nil {
		t.Fatalf("encode management config: %v", err)
	}
	return body
}

type addressedMemoryListener struct {
	*memoryListener
	clientAddress net.Addr
}

func newAddressedMemoryListener(address *net.TCPAddr) *addressedMemoryListener {
	listener := newMemoryListener()
	listener.address = address
	return &addressedMemoryListener{
		memoryListener: listener,
		clientAddress: &net.TCPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 53131,
		},
	}
}

func (listener *addressedMemoryListener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	clientPipe, serverPipe := net.Pipe()
	client := &addressedConn{Conn: clientPipe, local: listener.clientAddress, remote: listener.Addr()}
	server := &addressedConn{Conn: serverPipe, local: listener.Addr(), remote: listener.clientAddress}
	select {
	case listener.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	case <-listener.closed:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	}
}

type addressedConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (connection *addressedConn) LocalAddr() net.Addr  { return connection.local }
func (connection *addressedConn) RemoteAddr() net.Addr { return connection.remote }

type cancelAwareConfigRepository struct {
	snapshot config.Snapshot
	started  chan struct{}
}

func (repository *cancelAwareConfigRepository) Read() (config.Snapshot, error) {
	return config.Snapshot{Config: repository.snapshot.Config.Clone(), Revision: repository.snapshot.Revision}, nil
}

func (*cancelAwareConfigRepository) Replace(config.Revision, config.Config) (config.Change, error) {
	return config.Change{}, errors.New("legacy Replace must not be called")
}

func (*cancelAwareConfigRepository) Rollback(config.Change) (config.Change, error) {
	return config.Change{}, errors.New("legacy Rollback must not be called")
}

func (repository *cancelAwareConfigRepository) ReplaceContext(ctx context.Context, _ config.Revision, _ config.Config) (config.Change, error) {
	close(repository.started)
	<-ctx.Done()
	return config.Change{}, ctx.Err()
}

func (*cancelAwareConfigRepository) RollbackContext(context.Context, config.Change) (config.Change, error) {
	return config.Change{}, errors.New("unexpected rollback")
}
