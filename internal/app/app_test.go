package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestRunServesManagementRoutesWithDisabledCaptureAndMIDI(t *testing.T) {
	configuration := testConfig()
	configuration.Capture.Enabled = false
	configuration.MIDI.Enabled = false

	listenerReady := make(chan net.Listener, 1)
	var nativeCalls atomic.Int32
	dependencies := Dependencies{
		Interfaces: func() ([]capture.Interface, error) {
			nativeCalls.Add(1)
			return nil, errors.New("unexpected interface discovery")
		},
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			nativeCalls.Add(1)
			return nil, errors.New("unexpected capture open")
		},
		NewMIDIDriver: func() (midi.Driver, error) {
			nativeCalls.Add(1)
			return nil, errors.New("unexpected MIDI initialization")
		},
		Listen: notifyingListen(t, listenerReady, nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
	listener := awaitListener(t, listenerReady, done)

	assertHTTP(t, listener, "/healthz", http.StatusOK, "ok\n")
	eventuallyHTTP(t, listener, "/readyz", http.StatusOK, "ok\n")
	assertHTTP(t, listener, "/metrics", http.StatusOK, "musical_packets_packet_queue_capacity")

	cancel()
	if err := await(t, done); err != nil {
		t.Fatalf("RunWithDependencies() error = %v, want nil", err)
	}
	if got := nativeCalls.Load(); got != 0 {
		t.Fatalf("disabled native boundary calls = %d, want 0", got)
	}
}

func TestRunReadinessTracksOptionalMIDIDevice(t *testing.T) {
	configuration := testConfig()
	configuration.Capture.Enabled = false
	configuration.MIDI.PollInterval = 5 * time.Millisecond

	listenerReady := make(chan net.Listener, 1)
	driver := &fakeDriver{}
	dependencies := Dependencies{
		NewMIDIDriver: func() (midi.Driver, error) { return driver, nil },
		Listen:        notifyingListen(t, listenerReady, nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
	listener := awaitListener(t, listenerReady, done)

	assertHTTP(t, listener, "/healthz", http.StatusOK, "ok\n")
	assertHTTP(t, listener, "/readyz", http.StatusServiceUnavailable, midi.ErrOutputUnavailable.Error())

	driver.setDevices([]midi.Device{{Number: 7, Name: "test output"}})
	eventuallyHTTP(t, listener, "/readyz", http.StatusOK, "ok\n")

	cancel()
	if err := await(t, done); err != nil {
		t.Fatalf("RunWithDependencies() error = %v, want nil", err)
	}
	if !driver.closed.Load() {
		t.Fatal("MIDI driver was not closed")
	}
}

func TestRunShutdownWhileMIDIIsUnavailable(t *testing.T) {
	tests := []struct {
		name     string
		closeErr error
	}{
		{name: "clean"},
		{name: "driver close failure", closeErr: errors.New("driver close failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := testConfig()
			configuration.Capture.Enabled = false
			driver := &fakeDriver{closeErr: test.closeErr}
			listenerReady := make(chan net.Listener, 1)
			dependencies := Dependencies{
				NewMIDIDriver: func() (midi.Driver, error) { return driver, nil },
				Listen:        notifyingListen(t, listenerReady, nil),
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
			listener := awaitListener(t, listenerReady, done)
			assertHTTP(t, listener, "/healthz", http.StatusOK, "ok\n")
			assertHTTP(t, listener, "/readyz", http.StatusServiceUnavailable, midi.ErrOutputUnavailable.Error())
			cancel()
			err := await(t, done)
			if test.closeErr == nil && err != nil {
				t.Fatalf("RunWithDependencies() error = %v, want nil", err)
			}
			if test.closeErr != nil && !errors.Is(err, test.closeErr) {
				t.Fatalf("RunWithDependencies() error = %v, want %v", err, test.closeErr)
			}
		})
	}
}

func TestRunPipelineWaitsForMIDIAndExcludesHTTPFeedback(t *testing.T) {
	configuration := testConfig()
	configuration.Mapping.DefaultState = config.FlowPlay
	configuration.Mapping.MinimumDuration = 2 * time.Second
	configuration.Mapping.MaximumDuration = 2 * time.Second
	configuration.Capture.BPF = "ip"

	var log operationLog
	listenerReady := make(chan net.Listener, 1)
	driver := &fakeDriver{
		devices: []midi.Device{{Number: 3, Name: "synth"}},
		log:     &log,
	}
	var opened capture.LiveConfig
	dependencies := Dependencies{
		Interfaces: func() ([]capture.Interface, error) {
			return []capture.Interface{{Name: "test0", Up: true, Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")}}}, nil
		},
		OpenLive: func(live capture.LiveConfig) (capture.Source, error) {
			opened = live
			_, portText, err := net.SplitHostPort((<-listenerReady).Addr().String())
			if err != nil {
				return nil, err
			}
			port, err := net.LookupPort("tcp", portText)
			if err != nil {
				return nil, err
			}
			return &sliceSource{
				events: []packet.Event{
					testPacket(41000, 443, time.Unix(100, 0)),
					testPacket(uint16(port), 443, time.Unix(101, 0)),
					testPacket(41001, uint16(port), time.Unix(102, 0)),
				},
				log: &log,
			}, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) { return driver, nil },
		Listen:        notifyingListen(t, listenerReady, &log),
	}

	err := RunWithDependencies(context.Background(), configuration, dependencies)
	if err == nil || !strings.Contains(err.Error(), "packet pipeline stopped unexpectedly") {
		t.Fatalf("RunWithDependencies() error = %v, want unexpected finite pipeline error", err)
	}
	if opened.Device != "test0" {
		t.Fatalf("capture device = %q, want test0", opened.Device)
	}
	if !strings.HasPrefix(opened.BPF, "(ip) and (") || !strings.Contains(opened.BPF, "tcp src port") || !strings.Contains(opened.BPF, "tcp dst port") {
		t.Fatalf("capture BPF = %q, want configured filter plus HTTP exclusion", opened.BPF)
	}

	operations := log.snapshot()
	if got := countPrefix(operations, "midi.send:90"); got != 1 {
		t.Fatalf("Note On count = %d, want 1; operations = %v", got, operations)
	}
	assertOrdered(t, operations, "midi.open", "midi.send:90", "capture.close", "midi.send:b0", "midi.output.close", "midi.driver.close")
}

func TestRunDefaultMonitorDoesNotEmitMIDI(t *testing.T) {
	configuration := testConfig()
	delivered := make(chan struct{})
	var log operationLog
	driver := &fakeDriver{devices: []midi.Device{{Number: 4, Name: "synth"}}, log: &log}
	listenerReady := make(chan net.Listener, 1)
	dependencies := Dependencies{
		Interfaces: testInterfaces,
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			return &blockingAfterSource{event: testPacket(41000, 443, time.Unix(200, 0)), delivered: delivered, log: &log}, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) { return driver, nil },
		Listen:        notifyingListen(t, listenerReady, &log),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
	listener := awaitListener(t, listenerReady, done)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("source packet was not consumed")
	}
	eventuallyHTTP(t, listener, "/metrics", http.StatusOK,
		`musical_packets_flow_selections_total{state="monitor",tier="default"} 1`)
	cancel()
	if err := await(t, done); err != nil {
		t.Fatalf("RunWithDependencies() error = %v, want nil", err)
	}
	if got := countPrefix(log.snapshot(), "midi.send:90"); got != 0 {
		t.Fatalf("default monitor Note On count = %d, want 0", got)
	}
}

func TestRunMIDIDisabledDropsSelectedNotesObservably(t *testing.T) {
	configuration := testConfig()
	configuration.MIDI.Enabled = false
	configuration.Mapping.DefaultState = config.FlowPlay
	delivered := make(chan struct{})
	listenerReady := make(chan net.Listener, 1)
	var midiFactoryCalls atomic.Int32
	dependencies := Dependencies{
		Interfaces: testInterfaces,
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			return &blockingAfterSource{event: testPacket(41000, 443, time.Unix(300, 0)), delivered: delivered}, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) {
			midiFactoryCalls.Add(1)
			return nil, errors.New("unexpected MIDI initialization")
		},
		Listen: notifyingListen(t, listenerReady, nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithDependencies(ctx, configuration, dependencies) }()
	listener := awaitListener(t, listenerReady, done)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("source packet was not consumed")
	}
	eventuallyHTTP(t, listener, "/metrics", http.StatusOK,
		`musical_packets_packet_events_dropped_total{reason="write_error",stage="note_sink"} 1`)
	cancel()
	if err := await(t, done); err != nil {
		t.Fatalf("RunWithDependencies() error = %v, want nil", err)
	}
	if got := midiFactoryCalls.Load(); got != 0 {
		t.Fatalf("MIDI factory calls = %d, want 0", got)
	}
}

func TestRunRollsBackStartupInLifecycleOrder(t *testing.T) {
	configuration := testConfig()
	var log operationLog
	listenerReady := make(chan net.Listener, 1)
	driver := &fakeDriver{
		devices: []midi.Device{{Number: 1, Name: "synth"}},
		log:     &log,
	}
	openErr := errors.New("capture permission denied")
	dependencies := Dependencies{
		Interfaces: func() ([]capture.Interface, error) {
			return []capture.Interface{{Name: "test0", Up: true, Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")}}}, nil
		},
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			log.add("capture.open")
			return nil, openErr
		},
		NewMIDIDriver: func() (midi.Driver, error) { return driver, nil },
		Listen:        notifyingListen(t, listenerReady, &log),
	}

	err := RunWithDependencies(context.Background(), configuration, dependencies)
	if !errors.Is(err, openErr) {
		t.Fatalf("RunWithDependencies() error = %v, want %v", err, openErr)
	}
	operations := log.snapshot()
	assertOrdered(t, operations, "http.listen", "midi.devices", "midi.open", "capture.open", "midi.send:b0", "midi.output.close", "midi.driver.close", "http.listener.close")
}

func TestRunBindFailureDoesNotStartCaptureOrMIDI(t *testing.T) {
	configuration := testConfig()
	bindErr := errors.New("address already in use")
	var boundaryCalls atomic.Int32
	dependencies := Dependencies{
		Interfaces: func() ([]capture.Interface, error) {
			boundaryCalls.Add(1)
			return nil, nil
		},
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			boundaryCalls.Add(1)
			return nil, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) {
			boundaryCalls.Add(1)
			return nil, nil
		},
		Listen: func(string, string) (net.Listener, error) { return nil, bindErr },
	}

	err := RunWithDependencies(context.Background(), configuration, dependencies)
	if !errors.Is(err, bindErr) {
		t.Fatalf("RunWithDependencies() error = %v, want %v", err, bindErr)
	}
	if got := boundaryCalls.Load(); got != 0 {
		t.Fatalf("capture/MIDI boundary calls = %d, want 0", got)
	}
}

func TestRunServerFailureClosesMIDI(t *testing.T) {
	configuration := testConfig()
	configuration.Capture.Enabled = false
	serveErr := errors.New("listener failed")
	var log operationLog
	driver := &fakeDriver{devices: []midi.Device{{Number: 2, Name: "synth"}}, log: &log}
	dependencies := Dependencies{
		NewMIDIDriver: func() (midi.Driver, error) { return driver, nil },
		Listen: func(string, string) (net.Listener, error) {
			log.add("http.listen")
			return &trackingListener{
				Listener: &failingListener{address: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43124}, err: serveErr},
				log:      &log,
			}, nil
		},
	}

	err := RunWithDependencies(context.Background(), configuration, dependencies)
	if !errors.Is(err, serveErr) {
		t.Fatalf("RunWithDependencies() error = %v, want %v", err, serveErr)
	}
	operations := log.snapshot()
	assertOrdered(t, operations, "midi.open", "midi.send:b0", "midi.output.close", "midi.driver.close")
	if countPrefix(operations, "http.listener.close") != 1 {
		t.Fatalf("operations = %v, want listener closed once", operations)
	}
}

func TestRunAlreadyCanceledDoesNotOpenBoundaries(t *testing.T) {
	configuration := testConfig()
	var boundaryCalls atomic.Int32
	called := func() { boundaryCalls.Add(1) }
	dependencies := Dependencies{
		Interfaces: func() ([]capture.Interface, error) { called(); return nil, nil },
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			called()
			return nil, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) { called(); return nil, nil },
		Listen: func(string, string) (net.Listener, error) {
			called()
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunWithDependencies(ctx, configuration, dependencies); err != nil {
		t.Fatalf("RunWithDependencies() error = %v, want nil", err)
	}
	if got := boundaryCalls.Load(); got != 0 {
		t.Fatalf("boundary calls = %d, want 0", got)
	}
}

func TestNormalizeComponentErrorPreservesCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("close failed")
	err := normalizeComponentError(fmt.Errorf("wrapped: %w", errors.Join(context.Canceled, cleanupErr)))
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("normalizeComponentError() = %v, want cleanup failure", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("normalizeComponentError() = %v, want cancellation removed", err)
	}
}

func testInterfaces() ([]capture.Interface, error) {
	return []capture.Interface{{
		Name: "test0", Up: true,
		Addresses: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")},
	}}, nil
}

func testConfig() config.Config {
	configuration := config.Default()
	configuration.Server.ListenAddress = "127.0.0.1:0"
	configuration.Server.ReadTimeout = time.Second
	configuration.Server.WriteTimeout = time.Second
	configuration.MIDI.PollInterval = 10 * time.Millisecond
	return configuration
}

func testPacket(sourcePort, destinationPort uint16, capturedAt time.Time) packet.Event {
	return packet.Event{
		CapturedAt:     capturedAt,
		Source:         packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: sourcePort},
		Destination:    packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: destinationPort},
		Protocol:       packet.ProtocolTCP,
		WireLength:     128,
		CapturedLength: 128,
		PayloadLength:  80,
	}
}

type sliceSource struct {
	mu     sync.Mutex
	events []packet.Event
	next   int
	log    *operationLog
}

type blockingAfterSource struct {
	once      sync.Once
	event     packet.Event
	delivered chan struct{}
	log       *operationLog
}

func (source *blockingAfterSource) Next(ctx context.Context) (packet.Event, error) {
	delivered := false
	source.once.Do(func() {
		delivered = true
		if source.delivered != nil {
			close(source.delivered)
		}
	})
	if delivered {
		return source.event, nil
	}
	<-ctx.Done()
	return packet.Event{}, ctx.Err()
}

func (source *blockingAfterSource) Close() error {
	if source.log != nil {
		source.log.add("capture.close")
	}
	return nil
}

func (source *sliceSource) Next(context.Context) (packet.Event, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.next == len(source.events) {
		return packet.Event{}, io.EOF
	}
	event := source.events[source.next]
	source.next++
	return event, nil
}

func (source *sliceSource) Close() error {
	if source.log != nil {
		source.log.add("capture.close")
	}
	return nil
}

type fakeDriver struct {
	mu       sync.Mutex
	devices  []midi.Device
	log      *operationLog
	closed   atomic.Bool
	closeErr error
}

func (driver *fakeDriver) setDevices(devices []midi.Device) {
	driver.mu.Lock()
	driver.devices = append([]midi.Device(nil), devices...)
	driver.mu.Unlock()
}

func (driver *fakeDriver) Devices() ([]midi.Device, error) {
	if driver.log != nil {
		driver.log.add("midi.devices")
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return append([]midi.Device(nil), driver.devices...), nil
}

func (driver *fakeDriver) Open(int) (midi.Output, error) {
	if driver.log != nil {
		driver.log.add("midi.open")
	}
	return &fakeOutput{log: driver.log}, nil
}

func (driver *fakeDriver) Close() error {
	if driver.log != nil {
		driver.log.add("midi.driver.close")
	}
	driver.closed.Store(true)
	return driver.closeErr
}

type fakeOutput struct{ log *operationLog }

func (output *fakeOutput) Send(message []byte) error {
	if output.log != nil {
		output.log.add(fmt.Sprintf("midi.send:%x", message[0]))
	}
	return nil
}

func (output *fakeOutput) Close() error {
	if output.log != nil {
		output.log.add("midi.output.close")
	}
	return nil
}

type trackingListener struct {
	net.Listener
	log *operationLog
}

func (listener *trackingListener) Close() error {
	if listener.log != nil {
		listener.log.add("http.listener.close")
	}
	return listener.Listener.Close()
}

func (listener *trackingListener) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer, ok := listener.Listener.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	})
	if !ok {
		return nil, errors.New("test listener does not support dialing")
	}
	return dialer.DialContext(ctx, network, address)
}

type memoryListener struct {
	address     net.Addr
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

type failingListener struct {
	address net.Addr
	err     error
}

func (listener *failingListener) Accept() (net.Conn, error) { return nil, listener.err }
func (listener *failingListener) Close() error              { return nil }
func (listener *failingListener) Addr() net.Addr            { return listener.address }

func newMemoryListener() *memoryListener {
	return &memoryListener{
		address:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43123},
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
	}
}

func (listener *memoryListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *memoryListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (listener *memoryListener) Addr() net.Addr { return listener.address }

func (listener *memoryListener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
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

func notifyingListen(t *testing.T, ready chan<- net.Listener, log *operationLog) func(string, string) (net.Listener, error) {
	t.Helper()
	return func(string, string) (net.Listener, error) {
		tracked := &trackingListener{Listener: newMemoryListener(), log: log}
		if log != nil {
			log.add("http.listen")
		}
		ready <- tracked
		return tracked, nil
	}
}

type operationLog struct {
	mu         sync.Mutex
	operations []string
}

func (log *operationLog) add(operation string) {
	log.mu.Lock()
	log.operations = append(log.operations, operation)
	log.mu.Unlock()
}

func (log *operationLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.operations...)
}

func assertHTTP(t *testing.T, listener net.Listener, path string, status int, bodyContains string) {
	t.Helper()
	client := listenerHTTPClient(t, listener)
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := client.Get("http://" + listener.Addr().String() + path)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("GET %s: read body: %v", path, readErr)
			}
			if response.StatusCode != status || !strings.Contains(string(body), bodyContains) {
				t.Fatalf("GET %s = %d, %q; want %d containing %q", path, response.StatusCode, body, status, bodyContains)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", path, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func eventuallyHTTP(t *testing.T, listener net.Listener, path string, status int, bodyContains string) {
	t.Helper()
	client := listenerHTTPClient(t, listener)
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + listener.Addr().String() + path)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode == status && strings.Contains(string(body), bodyContains) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("GET %s did not eventually return %d containing %q", path, status, bodyContains)
}

func listenerHTTPClient(t *testing.T, listener net.Listener) *http.Client {
	t.Helper()
	dialer, ok := listener.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	})
	if !ok {
		t.Fatal("test listener does not support dialing")
	}
	return &http.Client{Transport: &http.Transport{
		DialContext:       dialer.DialContext,
		DisableKeepAlives: true,
	}}
}

func await(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
		return nil
	}
}

func awaitListener(t *testing.T, ready <-chan net.Listener, done <-chan error) net.Listener {
	t.Helper()
	select {
	case listener := <-ready:
		return listener
	case err := <-done:
		t.Fatalf("application stopped before listening: %v", err)
		return nil
	case <-time.After(2 * time.Second):
		t.Fatal("application did not bind its HTTP listener")
		return nil
	}
}

func countPrefix(values []string, prefix string) int {
	var count int
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			count++
		}
	}
	return count
}

func assertOrdered(t *testing.T, operations []string, wanted ...string) {
	t.Helper()
	position := 0
	for _, operation := range operations {
		if strings.HasPrefix(operation, wanted[position]) {
			position++
			if position == len(wanted) {
				return
			}
		}
	}
	t.Fatalf("operations = %v, want ordered prefixes %v", operations, wanted)
}
