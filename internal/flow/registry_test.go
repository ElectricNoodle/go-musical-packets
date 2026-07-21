package flow

import (
	"net/netip"
	"reflect"
	"sort"
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
	if second.Flow.LastEvent != reverse {
		t.Fatalf("LastEvent = %#v, want newest reverse event %#v", second.Flow.LastEvent, reverse)
	}
	late := registryEvent(now.Add(500*time.Millisecond), a, b, 50)
	third, err := registry.Observe(late)
	if err != nil {
		t.Fatalf("Observe(late) error = %v", err)
	}
	if third.Flow.LastEvent != reverse {
		t.Fatalf("late packet replaced LastEvent: got %#v, want %#v", third.Flow.LastEvent, reverse)
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

func TestRegistryRecentSnapshotsLimitsAndOrders(t *testing.T) {
	registry := testRegistry(t, 10, time.Minute)
	now := time.Unix(5000, 0)
	source := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1"), Port: 50000}

	oldest, err := registry.Observe(registryEvent(now, source, packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.1"), Port: 443}, 64))
	if err != nil {
		t.Fatalf("Observe(oldest) error = %v", err)
	}
	tieA, err := registry.Observe(registryEvent(now.Add(time.Second), source, packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.2"), Port: 443}, 64))
	if err != nil {
		t.Fatalf("Observe(tie A) error = %v", err)
	}
	tieB, err := registry.Observe(registryEvent(now.Add(time.Second), source, packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.3"), Port: 443}, 64))
	if err != nil {
		t.Fatalf("Observe(tie B) error = %v", err)
	}
	newest, err := registry.Observe(registryEvent(now.Add(2*time.Second), source, packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.4"), Port: 443}, 64))
	if err != nil {
		t.Fatalf("Observe(newest) error = %v", err)
	}

	tieIDs := []string{tieA.Flow.ID, tieB.Flow.ID}
	sort.Strings(tieIDs)
	got, total := registry.RecentSnapshots(3)
	if total != 4 {
		t.Fatalf("RecentSnapshots() total = %d, want 4", total)
	}
	wantIDs := []string{newest.Flow.ID, tieIDs[0], tieIDs[1]}
	if gotIDs := snapshotIDs(got); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("RecentSnapshots() IDs = %v, want %v", gotIDs, wantIDs)
	}
	for _, snapshot := range got {
		if snapshot.ID == oldest.Flow.ID {
			t.Fatalf("RecentSnapshots() included oldest flow %q", oldest.Flow.ID)
		}
	}
}

func TestRegistryRecentSnapshotsLimitBoundariesAndFullCompatibility(t *testing.T) {
	registry := testRegistry(t, 4, time.Minute)
	now := time.Unix(6000, 0)
	source := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1")}
	for _, destination := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if _, err := registry.Observe(registryEvent(now, source, packet.Endpoint{Addr: netip.MustParseAddr(destination)}, 64)); err != nil {
			t.Fatalf("Observe(%s) error = %v", destination, err)
		}
		now = now.Add(time.Second)
	}

	for _, limit := range []int{-1, 0} {
		got, total := registry.RecentSnapshots(limit)
		if got == nil || len(got) != 0 || total != 3 {
			t.Fatalf("RecentSnapshots(%d) = %#v, %d; want non-nil empty, 3", limit, got, total)
		}
	}

	got, total := registry.RecentSnapshots(100)
	if total != 3 {
		t.Fatalf("RecentSnapshots(100) total = %d, want 3", total)
	}
	if full := registry.Snapshots(); !reflect.DeepEqual(got, full) {
		t.Fatalf("RecentSnapshots(100) = %#v, Snapshots() = %#v", got, full)
	}
}

func TestRegistryRecentSnapshotsReturnsDetachedValues(t *testing.T) {
	registry := testRegistry(t, 2, time.Minute)
	event := registryEvent(
		time.Unix(7000, 0),
		packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1"), Port: 50000},
		packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.1"), Port: 443},
		128,
	)
	observed, err := registry.Observe(event)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	got, total := registry.RecentSnapshots(1)
	if len(got) != 1 || total != 1 {
		t.Fatalf("RecentSnapshots(1) = %#v, %d; want one flow", got, total)
	}
	got[0].ID = "changed"
	got[0].Key.A = packet.Endpoint{}
	got[0].Packets = 999

	current, ok := registry.Get(observed.Flow.ID)
	if !ok {
		t.Fatalf("Get(%q) did not find flow", observed.Flow.ID)
	}
	if !reflect.DeepEqual(current, observed.Flow) {
		t.Fatalf("mutating RecentSnapshots() result changed registry: got %#v, want %#v", current, observed.Flow)
	}
}

func TestRegistryRecentSnapshotsConcurrentObserve(t *testing.T) {
	registry := testRegistry(t, 32, time.Minute)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for worker := range 8 {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			for observation := range 200 {
				destination := packet.Endpoint{
					Addr: netip.AddrFrom4([4]byte{198, 51, 100, byte(worker*4 + observation%4 + 1)}),
					Port: uint16(4000 + worker),
				}
				_, err := registry.Observe(registryEvent(
					time.Unix(8000+int64(observation), 0),
					packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1"), Port: uint16(50000 + worker)},
					destination,
					64,
				))
				if err != nil {
					t.Errorf("Observe() error = %v", err)
					return
				}
			}
		}(worker)
	}
	close(start)
	for range 200 {
		got, total := registry.RecentSnapshots(7)
		if len(got) > 7 || total > 32 || len(got) > total {
			t.Fatalf("RecentSnapshots(7) length = %d, total = %d", len(got), total)
		}
		for index := 1; index < len(got); index++ {
			if snapshotBefore(got[index], got[index-1]) {
				t.Fatalf("RecentSnapshots(7) is out of order at %d: %#v", index, got)
			}
		}
	}
	wait.Wait()
}

func snapshotIDs(snapshots []Snapshot) []string {
	ids := make([]string, len(snapshots))
	for index, snapshot := range snapshots {
		ids[index] = snapshot.ID
	}
	return ids
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
