package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestManagementBackendFlowsPagesAndConvertsSnapshots(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, nil, nil)
	registry := newManagementFlowRegistry(t, configuration)
	firstEvent := testPacket(41000, 443, time.Unix(100, 0))
	secondEvent := testPacket(41001, 53, time.Unix(200, 0))
	thirdEvent := testPacket(41002, 80, time.Unix(300, 0))
	first, err := registry.Observe(firstEvent)
	if err != nil {
		t.Fatalf("Observe(first) error = %v", err)
	}
	if _, err := registry.Observe(secondEvent); err != nil {
		t.Fatalf("Observe(second) error = %v", err)
	}
	third, err := registry.Observe(thirdEvent)
	if err != nil {
		t.Fatalf("Observe(third) error = %v", err)
	}
	reverse := firstEvent
	reverse.CapturedAt = time.Unix(400, 0)
	reverse.Source, reverse.Destination = reverse.Destination, reverse.Source
	updatedFirst, err := registry.Observe(reverse)
	if err != nil {
		t.Fatalf("Observe(reverse) error = %v", err)
	}
	if _, err := controller.ReplaceMute(map[string]struct{}{first.Flow.ID: {}}); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	if _, err := controller.ReplaceSolo(map[string]struct{}{third.Flow.ID: {}}); err != nil {
		t.Fatalf("ReplaceSolo() error = %v", err)
	}
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background(), registry)

	page, err := backend.Flows(context.Background(), managementapi.FlowPageRequest{Limit: 2})
	if err != nil {
		t.Fatalf("Flows() error = %v", err)
	}
	if page.Total != 3 || page.Limit != 2 || !page.Truncated || len(page.Flows) != 2 {
		t.Fatalf("Flows() page = %#v, want total 3 limited and truncated to 2", page)
	}
	if page.Flows == nil || page.Overlay.Muted == nil || page.Overlay.Soloed == nil {
		t.Fatalf("Flows() returned nil collections: %#v", page)
	}
	if page.Flows[0].ID != first.Flow.ID || page.Flows[1].ID != third.Flow.ID {
		t.Fatalf("Flows() order = [%s %s], want newest [%s %s]", page.Flows[0].ID, page.Flows[1].ID, first.Flow.ID, third.Flow.ID)
	}
	gotFirst := page.Flows[0]
	if !gotFirst.Muted || gotFirst.Soloed || gotFirst.Packets != 2 || gotFirst.PacketsAToB != 1 || gotFirst.PacketsBToA != 1 {
		t.Fatalf("converted first flow = %#v, want muted bidirectional counters", gotFirst)
	}
	if gotFirst.Protocol != string(updatedFirst.Flow.Key.Protocol) ||
		gotFirst.EndpointA.Address != updatedFirst.Flow.Key.A.Addr.String() ||
		gotFirst.EndpointA.Port != updatedFirst.Flow.Key.A.Port ||
		gotFirst.EndpointB.Address != updatedFirst.Flow.Key.B.Addr.String() ||
		gotFirst.EndpointB.Port != updatedFirst.Flow.Key.B.Port ||
		!gotFirst.FirstSeen.Equal(updatedFirst.Flow.FirstSeen) ||
		!gotFirst.LastSeen.Equal(updatedFirst.Flow.LastSeen) {
		t.Fatalf("converted first flow = %#v, want snapshot %#v", gotFirst, updatedFirst.Flow)
	}
	if !page.Flows[1].Soloed || page.Flows[1].Muted {
		t.Fatalf("converted third flow = %#v, want soloed only", page.Flows[1])
	}
	if !reflect.DeepEqual(page.Overlay.Muted, []string{first.Flow.ID}) || !reflect.DeepEqual(page.Overlay.Soloed, []string{third.Flow.ID}) {
		t.Fatalf("Flows() overlay = %#v", page.Overlay)
	}

	for _, limit := range []int{0, maximumManagementFlowPageLimit + 1} {
		_, err := backend.Flows(context.Background(), managementapi.FlowPageRequest{Limit: limit})
		assertManagementBackendError(t, err, managementapi.ErrorInvalid, "invalid_flow_page")
	}
}

func TestManagementBackendFlowOverlayReplacementValidationAndReadOnlyUse(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Performance.FlowRegistryCapacity = 2
	controller := mustController(t, configuration, nil, nil)
	if controller.Current().Writable {
		t.Fatal("test controller is writable, want read-only runtime")
	}
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	a := strings.Repeat("a", 24)
	b := strings.Repeat("b", 24)
	c := strings.Repeat("c", 24)

	overlay, err := backend.SetMutedFlows(context.Background(), []string{b, a})
	if err != nil {
		t.Fatalf("SetMutedFlows() on read-only runtime error = %v", err)
	}
	if !reflect.DeepEqual(overlay.Muted, []string{a, b}) || overlay.Soloed == nil || len(overlay.Soloed) != 0 {
		t.Fatalf("SetMutedFlows() = %#v, want sorted mute set and non-nil empty solo set", overlay)
	}
	overlay, err = backend.SetSoloedFlows(context.Background(), []string{c})
	if err != nil {
		t.Fatalf("SetSoloedFlows() error = %v", err)
	}
	if !reflect.DeepEqual(overlay.Muted, []string{a, b}) || !reflect.DeepEqual(overlay.Soloed, []string{c}) {
		t.Fatalf("SetSoloedFlows() = %#v, want mute set preserved", overlay)
	}
	overlay, err = backend.SetMutedFlows(context.Background(), []string{c})
	if err != nil {
		t.Fatalf("SetMutedFlows(replace) error = %v", err)
	}
	if !reflect.DeepEqual(overlay.Muted, []string{c}) || !reflect.DeepEqual(overlay.Soloed, []string{c}) {
		t.Fatalf("SetMutedFlows(replace) = %#v, want replacement with solo preserved", overlay)
	}
	overlay, err = backend.SetMutedFlows(context.Background(), []string{})
	if err != nil {
		t.Fatalf("SetMutedFlows(clear) error = %v", err)
	}
	if overlay.Muted == nil || len(overlay.Muted) != 0 || !reflect.DeepEqual(overlay.Soloed, []string{c}) {
		t.Fatalf("SetMutedFlows(clear) = %#v, want non-nil empty mute set", overlay)
	}

	invalidInputs := []struct {
		name string
		ids  []string
	}{
		{name: "missing array", ids: nil},
		{name: "duplicate", ids: []string{a, a}},
		{name: "invalid ID", ids: []string{"INVALID"}},
		{name: "over capacity", ids: []string{a, b, c}},
	}
	for _, test := range invalidInputs {
		t.Run(test.name, func(t *testing.T) {
			before := controller.Overlay()
			_, err := backend.SetSoloedFlows(context.Background(), test.ids)
			assertManagementBackendError(t, err, managementapi.ErrorInvalid, "invalid_flow_set")
			after := controller.Overlay()
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid flow set changed overlay: got %#v, want %#v", after, before)
			}
		})
	}

	overlay, err = backend.SetSoloedFlows(context.Background(), []string{})
	if err != nil {
		t.Fatalf("SetSoloedFlows(clear) error = %v", err)
	}
	if overlay.Muted == nil || overlay.Soloed == nil || len(overlay.Muted) != 0 || len(overlay.Soloed) != 0 {
		t.Fatalf("cleared overlay = %#v, want non-nil empty arrays", overlay)
	}
}

func TestManagementBackendConcurrentMuteAndSoloReplacementsPreserveBoth(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, nil, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	muted := []string{strings.Repeat("1", 24), strings.Repeat("2", 24)}
	soloed := []string{strings.Repeat("3", 24), strings.Repeat("4", 24)}
	start := make(chan struct{})
	errors := make(chan error, 2)
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		<-start
		_, err := backend.SetMutedFlows(context.Background(), muted)
		errors <- err
	}()
	go func() {
		defer writers.Done()
		<-start
		_, err := backend.SetSoloedFlows(context.Background(), soloed)
		errors <- err
	}()
	close(start)
	writers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent overlay replacement error = %v", err)
		}
	}
	got := managementFlowOverlay(controller.Overlay())
	if !reflect.DeepEqual(got.Muted, muted) || !reflect.DeepEqual(got.Soloed, soloed) {
		t.Fatalf("concurrent overlay = %#v, want muted %v soloed %v", got, muted, soloed)
	}
}

func TestManagementBackendFlowOperationsRejectUnavailableRuntime(t *testing.T) {
	configuration := managementTestConfig()
	registry := newManagementFlowRegistry(t, configuration)
	flowID := strings.Repeat("a", 24)

	t.Run("not ready", func(t *testing.T) {
		controller := mustController(t, configuration, nil, nil)
		var ready atomic.Bool
		backend := newTestManagementBackend(controller, &ready, context.Background(), registry)
		assertUnavailableFlowOperations(t, backend, controller, flowID, true)
	})

	t.Run("shutdown", func(t *testing.T) {
		controller := mustController(t, configuration, nil, nil)
		var ready atomic.Bool
		ready.Store(true)
		lifecycle, cancel := context.WithCancel(context.Background())
		cancel()
		backend := newTestManagementBackend(controller, &ready, lifecycle, registry)
		assertUnavailableFlowOperations(t, backend, controller, flowID, true)
	})

	for _, state := range []ControllerState{
		ControllerStateDurabilityUncertain,
		ControllerStateOutOfSync,
		ControllerStateDegraded,
	} {
		t.Run(string(state), func(t *testing.T) {
			controller := mustController(t, configuration, nil, nil)
			controller.store.publish(snapshotWithStatus(controller.store.current.Load(), state, "internal detail"))
			var ready atomic.Bool
			ready.Store(true)
			backend := newTestManagementBackend(controller, &ready, context.Background(), registry)
			if _, err := backend.Flows(context.Background(), managementapi.FlowPageRequest{Limit: 1}); err != nil {
				t.Fatalf("Flows() in state %q error = %v, want read available", state, err)
			}
			assertUnavailableFlowOperations(t, backend, controller, flowID, false)
		})
	}
}

func TestControllerOverlayContextCancellationPreventsPublication(t *testing.T) {
	controller := mustController(t, managementTestConfig(), nil, nil)
	flowID := strings.Repeat("a", 24)
	ctx, cancel := context.WithCancel(context.Background())
	controller.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := controller.ReplaceMuteContext(ctx, map[string]struct{}{flowID: {}})
		done <- err
	}()
	<-started
	cancel()
	controller.mu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReplaceMuteContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReplaceMuteContext() did not return after cancellation")
	}
	if got := controller.Overlay(); len(got.Muted) != 0 || len(got.Soloed) != 0 {
		t.Fatalf("canceled overlay publication = %#v, want empty", got)
	}
	if _, err := controller.ReplaceSoloContext(nil, map[string]struct{}{flowID: {}}); err == nil {
		t.Fatal("ReplaceSoloContext(nil) error = nil")
	}
}

func TestNewManagementBackendRequiresFlowRegistry(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, nil, nil)
	var ready atomic.Bool
	if _, err := newManagementBackend(controller, nil, nil, nil, &ready, context.Background()); err == nil || !strings.Contains(err.Error(), "flow registry") {
		t.Fatalf("newManagementBackend(nil registry) error = %v, want flow registry error", err)
	}
}

func TestNewManagementBackendRequiresInterfaceDiscovery(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, nil, nil)
	registry, err := flow.NewRegistry(flow.RegistryConfig{
		Seed: configuration.Mapping.Seed, Capacity: 8, TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	var ready atomic.Bool
	if _, err := newManagementBackend(controller, registry, nil, nil, &ready, context.Background()); err == nil || !strings.Contains(err.Error(), "interface discovery") {
		t.Fatalf("newManagementBackend(nil interfaces) error = %v, want interface discovery error", err)
	}
}

func TestRunServesReadOnlyFlowManagement(t *testing.T) {
	configuration := testConfig()
	configuration.MIDI.Enabled = false
	delivered := make(chan struct{})
	event := testPacket(41000, 443, time.Unix(1000, 0))
	listenerReady := make(chan net.Listener, 1)
	listener := newAddressedMemoryListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43134})
	dependencies := Dependencies{
		Interfaces: testInterfaces,
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			return &blockingAfterSource{event: event, delivered: delivered}, nil
		},
		Listen: func(string, string) (net.Listener, error) {
			tracked := &trackingListener{Listener: listener}
			listenerReady <- tracked
			return tracked, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
	bound := awaitListener(t, listenerReady, done)
	client := listenerHTTPClient(t, bound)
	defer client.CloseIdleConnections()
	awaitManagementReady(t, client, bound)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("capture event was not delivered")
	}

	page := awaitLiveFlowPage(t, client, bound, 1)
	if page.Total != 1 || len(page.Flows) != 1 || page.Truncated {
		t.Fatalf("GET flows = %#v, want one untruncated flow", page)
	}
	flowID := page.Flows[0].ID
	if flowID == "" || page.Flows[0].Protocol != string(packet.ProtocolTCP) {
		t.Fatalf("GET flow = %#v", page.Flows[0])
	}
	statusResponse := managementRequest(t, client, bound, http.MethodGet, "/api/v1/status", nil, nil)
	statusBody := readManagementBody(t, statusResponse)
	_ = statusResponse.Body.Close()
	var status managementapi.Status
	if statusResponse.StatusCode != http.StatusOK || json.Unmarshal(statusBody, &status) != nil || status.Writable {
		t.Fatalf("GET status = %d, %q; want read-only status", statusResponse.StatusCode, statusBody)
	}
	interfacesResponse := managementRequest(t, client, bound, http.MethodGet, "/api/v1/interfaces", nil, nil)
	interfacesBody := readManagementBody(t, interfacesResponse)
	_ = interfacesResponse.Body.Close()
	var interfaces managementapi.InterfacesDocument
	if interfacesResponse.StatusCode != http.StatusOK || json.Unmarshal(interfacesBody, &interfaces) != nil || interfaces.Selected == "" || len(interfaces.Interfaces) == 0 {
		t.Fatalf("GET interfaces = %d, %q; want selected capture interface", interfacesResponse.StatusCode, interfacesBody)
	}
	eventuallyHTTP(t, bound, "/metrics", http.StatusOK,
		`musical_packets_management_api_requests_total{method="GET",result="success",route="/api/v1/interfaces"} 1`)

	overlay := postLiveFlowSet(t, client, bound, "/api/v1/flows/mute", []string{flowID}, http.StatusOK, nil)
	if !reflect.DeepEqual(overlay.Muted, []string{flowID}) || overlay.Soloed == nil {
		t.Fatalf("POST mute = %#v", overlay)
	}
	otherID := strings.Repeat("f", 24)
	overlay = postLiveFlowSet(t, client, bound, "/api/v1/flows/solo", []string{otherID}, http.StatusOK, nil)
	if !reflect.DeepEqual(overlay.Muted, []string{flowID}) || !reflect.DeepEqual(overlay.Soloed, []string{otherID}) {
		t.Fatalf("POST solo = %#v, want mute preserved", overlay)
	}
	overlay = postLiveFlowSet(t, client, bound, "/api/v1/flows/mute", []string{}, http.StatusOK, nil)
	if overlay.Muted == nil || len(overlay.Muted) != 0 || !reflect.DeepEqual(overlay.Soloed, []string{otherID}) {
		t.Fatalf("POST mute clear = %#v", overlay)
	}
	postLiveFlowSet(t, client, bound, "/api/v1/flows/solo", []string{otherID, otherID}, http.StatusUnprocessableEntity, nil)
	postLiveFlowSet(t, client, bound, "/api/v1/flows/solo", []string{"INVALID"}, http.StatusUnprocessableEntity, []string{"flow_ids"})

	cancel()
	if err := await(t, done); err != nil {
		t.Fatalf("RunWithDependencies() error = %v, want nil", err)
	}
}

func newManagementFlowRegistry(t *testing.T, configuration config.Config) *flow.Registry {
	t.Helper()
	registry, err := flow.NewRegistry(flow.RegistryConfig{
		Seed:     configuration.Mapping.Seed,
		Capacity: configuration.Performance.FlowRegistryCapacity,
		TTL:      configuration.Performance.FlowTTL,
	})
	if err != nil {
		t.Fatalf("flow.NewRegistry() error = %v", err)
	}
	return registry
}

func assertUnavailableFlowOperations(t *testing.T, backend *managementBackend, controller *Controller, flowID string, includeRead bool) {
	t.Helper()
	before := controller.Overlay()
	if includeRead {
		_, err := backend.Flows(context.Background(), managementapi.FlowPageRequest{Limit: 1})
		assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")
	}
	_, err := backend.SetMutedFlows(context.Background(), []string{flowID})
	assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")
	_, err = backend.SetSoloedFlows(context.Background(), []string{flowID})
	assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")
	if got := controller.Overlay(); !reflect.DeepEqual(got, before) {
		t.Fatalf("unavailable operations changed overlay: got %#v, want %#v", got, before)
	}
}

func awaitLiveFlowPage(t *testing.T, client *http.Client, listener net.Listener, limit int) managementapi.FlowPage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response := managementRequest(t, client, listener, http.MethodGet, "/api/v1/flows?limit=1", nil, nil)
		body := readManagementBody(t, response)
		_ = response.Body.Close()
		var page managementapi.FlowPage
		if response.StatusCode == http.StatusOK && json.Unmarshal(body, &page) == nil && page.Total > 0 {
			if page.Limit != limit {
				t.Fatalf("GET flows limit = %d, want %d", page.Limit, limit)
			}
			return page
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("flow page did not contain captured event")
	return managementapi.FlowPage{}
}

func postLiveFlowSet(
	t *testing.T,
	client *http.Client,
	listener net.Listener,
	path string,
	flowIDs []string,
	wantStatus int,
	wantFields []string,
) managementapi.FlowOverlay {
	t.Helper()
	body, err := json.Marshal(struct {
		FlowIDs []string `json:"flow_ids"`
	}{FlowIDs: flowIDs})
	if err != nil {
		t.Fatalf("json.Marshal(flow set) error = %v", err)
	}
	response := managementRequest(t, client, listener, http.MethodPost, path, body, map[string]string{"Content-Type": "application/json"})
	responseBody := readManagementBody(t, response)
	_ = response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("POST %s = %d, %q; want %d", path, response.StatusCode, responseBody, wantStatus)
	}
	if wantStatus != http.StatusOK {
		assertManagementProblem(t, response.StatusCode, responseBody, wantStatus, "invalid_flow_set", wantFields)
		return managementapi.FlowOverlay{}
	}
	var overlay managementapi.FlowOverlay
	if err := json.Unmarshal(responseBody, &overlay); err != nil {
		t.Fatalf("decode POST %s response %q: %v", path, responseBody, err)
	}
	return overlay
}
