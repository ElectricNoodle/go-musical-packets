package peer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

type collectingSink struct {
	mu     sync.Mutex
	events []music.NoteEvent
	ready  chan struct{}
}

func (sink *collectingSink) Write(_ context.Context, event music.NoteEvent) error {
	sink.mu.Lock()
	sink.events = append(sink.events, event)
	sink.mu.Unlock()
	select {
	case sink.ready <- struct{}{}:
	default:
	}
	return nil
}

func TestEdgeAndHostExchangeAuthenticatedNote(t *testing.T) {
	sink := &collectingSink{ready: make(chan struct{}, 1)}
	host := mustHost(t, sink)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != Path {
			http.NotFound(response, request)
			return
		}
		host.ServeHTTP(response, request)
	}))
	defer server.Close()

	edge, err := NewEdge(EdgeConfig{
		URL:   strings.Replace(server.URL, "http://", "ws://", 1) + Path,
		Token: "sixteen-byte-token", InstanceID: "edge-1", MappingVersion: music.FlowModeV1,
		QueueCapacity: 4, StaleAfter: time.Second, ReconnectBase: 10 * time.Millisecond, ReconnectLimit: 20 * time.Millisecond,
		Jitter: func(duration time.Duration) time.Duration { return duration },
	})
	if err != nil {
		t.Fatalf("NewEdge() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- edge.Run(ctx) }()
	event := testNote(time.Now().UTC(), 13)
	if err := edge.Write(context.Background(), event); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	select {
	case <-sink.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("host did not accept note")
	}

	edgeSnapshot := edge.Snapshot().Outbound
	if edgeSnapshot == nil || edgeSnapshot.RemoteInstance != "host-1" || edgeSnapshot.SentTotal != 1 || len(edgeSnapshot.ActiveChannels) != 1 || edgeSnapshot.ActiveChannels[0] != 13 {
		t.Fatalf("edge snapshot = %#v", edgeSnapshot)
	}
	hostSnapshot := host.Snapshot()
	if len(hostSnapshot.Nodes) != 1 || hostSnapshot.Nodes[0].InstanceID != "edge-1" || hostSnapshot.Nodes[0].AcceptedTotal != 1 || hostSnapshot.Nodes[0].ActiveChannels[0] != 13 {
		t.Fatalf("host snapshot = %#v", hostSnapshot)
	}
	sink.mu.Lock()
	got := sink.events[0]
	sink.mu.Unlock()
	if got.Channel != 13 {
		t.Fatalf("host event channel = %d, want originating channel 13", got.Channel)
	}
	duplicate := event
	duplicate.CreatedAt = time.Now().UTC()
	if err := edge.Write(context.Background(), duplicate); err != nil {
		t.Fatalf("duplicate Write() error = %v", err)
	}
	eventually(t, func() bool { return host.Snapshot().Nodes[0].DuplicateTotal == 1 })
	stale := testNote(time.Now().Add(-time.Hour), 2)
	stale.ID = "edge-1:0123456789abcdef01234567:stale"
	if err := edge.Write(context.Background(), stale); err != nil {
		t.Fatalf("stale Write() error = %v", err)
	}
	eventually(t, func() bool { return edge.Snapshot().Outbound.DroppedStale == 1 })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestHostRejectsMissingAuthenticationAndBrowserOrigin(t *testing.T) {
	host := mustHost(t, &collectingSink{ready: make(chan struct{}, 1)})
	for _, test := range []struct {
		name   string
		header http.Header
		status int
	}{
		{name: "missing bearer", header: http.Header{}, status: http.StatusUnauthorized},
		{name: "browser origin", header: http.Header{"Authorization": {"Bearer sixteen-byte-token"}, "Origin": {"https://evil.example"}}, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://host.example"+Path, nil)
			request.Header = test.header
			response := httptest.NewRecorder()
			host.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestEdgeQueueIsBoundedAndDropsStaleBeforeSending(t *testing.T) {
	edge, err := NewEdge(EdgeConfig{
		URL: "ws://127.0.0.1:1" + Path, Token: "sixteen-byte-token", InstanceID: "edge-1", MappingVersion: music.FlowModeV1,
		QueueCapacity: 1, StaleAfter: time.Millisecond, ReconnectBase: time.Millisecond, ReconnectLimit: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewEdge() error = %v", err)
	}
	if err := edge.Write(context.Background(), testNote(time.Now().Add(-time.Hour), 1)); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if err := edge.Write(context.Background(), testNote(time.Now(), 2)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second Write() error = %v, want ErrQueueFull", err)
	}
	if got := edge.Snapshot().Outbound.DroppedFull; got != 1 {
		t.Fatalf("DroppedFull = %d, want 1", got)
	}
}

func TestEdgeSnapshotExposesSafeTargetOnly(t *testing.T) {
	edge, err := NewEdge(EdgeConfig{
		URL: "wss://host.example/api/v1/peer?token=url-secret#fragment", Token: "sixteen-byte-token",
		InstanceID: "edge-1", MappingVersion: music.FlowModeV1, QueueCapacity: 1,
		StaleAfter: time.Second, ReconnectBase: time.Millisecond, ReconnectLimit: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewEdge() error = %v", err)
	}
	target := edge.Snapshot().Outbound.Target
	if target != "wss://host.example/api/v1/peer" || strings.Contains(target, "secret") {
		t.Fatalf("safe target = %q", target)
	}
	unsafe := errors.New("request wss://host.example/api/v1/peer?token=url-secret#fragment failed with sixteen-byte-token")
	safe := edge.safeError(unsafe)
	if strings.Contains(safe, "url-secret") || strings.Contains(safe, "sixteen-byte-token") || strings.Contains(safe, "?token=") || strings.Contains(safe, "#fragment") {
		t.Fatalf("safe error exposed a credential: %q", safe)
	}
}

func TestEdgeReconnectsWithBoundedBackoff(t *testing.T) {
	edge, err := NewEdge(EdgeConfig{
		URL: "ws://127.0.0.1:1" + Path, Token: "sixteen-byte-token", InstanceID: "edge-1", MappingVersion: music.FlowModeV1,
		QueueCapacity: 1, StaleAfter: time.Second, ReconnectBase: time.Millisecond, ReconnectLimit: 2 * time.Millisecond,
		Jitter: func(duration time.Duration) time.Duration { return duration },
	})
	if err != nil {
		t.Fatalf("NewEdge() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- edge.Run(ctx) }()
	eventually(t, func() bool { return edge.Snapshot().Outbound.Reconnects >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	snapshot := edge.Snapshot().Outbound
	if snapshot.State != "disconnected" || snapshot.Reconnects < 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func mustHost(t *testing.T, sink NoteSink) *Host {
	t.Helper()
	host, err := NewHost(HostConfig{
		Token: "sixteen-byte-token", InstanceID: "host-1", MappingVersion: music.FlowModeV1,
		MaximumConnections: 4, RecentCapacity: 8, RecentTTL: time.Minute,
		StaleAfter: time.Second, DuplicateCapacity: 16, Sink: sink,
	})
	if err != nil {
		t.Fatalf("NewHost() error = %v", err)
	}
	return host
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied")
}
