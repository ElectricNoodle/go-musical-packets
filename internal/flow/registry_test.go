package flow

import (
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestRegistryCombinesDirections(t *testing.T) {
	registry := testRegistry(t, 10, time.Minute)
	now := time.Unix(1000, 0)
	a := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1"), Port: 50000}
	b := packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.1"), Port: 443}

	forward := registryEvent(now, a, b, 100)
	reverse := registryEvent(now.Add(time.Second), b, a, 200)
	first, err := registry.Observe(forward)
	if err != nil {
		t.Fatalf("Observe(forward) error = %v", err)
	}
	second, err := registry.Observe(reverse)
	if err != nil {
		t.Fatalf("Observe(reverse) error = %v", err)
	}

	if !first.Created || second.Created {
		t.Fatalf("created flags = %v, %v; want true, false", first.Created, second.Created)
	}
	if first.Flow.ID != second.Flow.ID || registry.Len() != 1 {
		t.Fatalf("directions were not combined: first=%s second=%s len=%d", first.Flow.ID, second.Flow.ID, registry.Len())
	}
	if second.Flow.Packets != 2 || second.Flow.Bytes != 300 || second.Flow.PacketsAToB != 1 || second.Flow.PacketsBToA != 1 {
		t.Fatalf("combined snapshot = %#v", second.Flow)
	}
}

func TestRegistryEvictsOldestAtCapacity(t *testing.T) {
	registry := testRegistry(t, 2, time.Minute)
	now := time.Unix(2000, 0)
	a := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1")}

	first, _ := registry.Observe(registryEvent(now, a, packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.2")}, 64))
	_, _ = registry.Observe(registryEvent(now.Add(time.Second), a, packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.3")}, 64))
	third, err := registry.Observe(registryEvent(now.Add(2*time.Second), a, packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.4")}, 64))
	if err != nil {
		t.Fatalf("Observe(third) error = %v", err)
	}

	if third.Evicted == nil || third.Evicted.ID != first.Flow.ID {
		t.Fatalf("evicted = %#v, want first flow %s", third.Evicted, first.Flow.ID)
	}
	if registry.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", registry.Len())
	}
}

func TestRegistryExpireUsesTTL(t *testing.T) {
	registry := testRegistry(t, 10, 10*time.Second)
	now := time.Unix(3000, 0)
	a := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1")}
	_, _ = registry.Observe(registryEvent(now, a, packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.2")}, 64))
	_, _ = registry.Observe(registryEvent(now.Add(5*time.Second), a, packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.3")}, 64))

	expired := registry.Expire(now.Add(11 * time.Second))
	if len(expired) != 1 {
		t.Fatalf("Expire() count = %d, want 1", len(expired))
	}
	if registry.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", registry.Len())
	}
}

func TestRegistryConcurrentObserve(t *testing.T) {
	registry := testRegistry(t, 10, time.Minute)
	event := registryEvent(
		time.Unix(4000, 0),
		packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1"), Port: 50000},
		packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.2"), Port: 443},
		128,
	)

	const observers = 100
	errors := make(chan error, observers)
	var wait sync.WaitGroup
	for range observers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := registry.Observe(event)
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	flows := registry.Snapshots()
	if len(flows) != 1 || flows[0].Packets != observers {
		t.Fatalf("Snapshots() = %#v, want one flow with %d packets", flows, observers)
	}
}

func testRegistry(t *testing.T, capacity int, ttl time.Duration) *Registry {
	t.Helper()
	registry, err := NewRegistry(RegistryConfig{Seed: "seed", Capacity: capacity, TTL: ttl})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

func registryEvent(at time.Time, source, destination packet.Endpoint, size int) packet.Event {
	return packet.Event{
		CapturedAt:     at,
		Source:         source,
		Destination:    destination,
		Protocol:       packet.ProtocolTCP,
		WireLength:     size,
		CapturedLength: size,
	}
}
