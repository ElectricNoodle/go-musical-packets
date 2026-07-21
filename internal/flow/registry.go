package flow

import (
	"container/heap"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

// RegistryConfig controls bounded flow observation.
type RegistryConfig struct {
	Seed     string
	Capacity int
	TTL      time.Duration
}

// Snapshot is an immutable view of an observed flow.
type Snapshot struct {
	ID          string
	Key         Key
	FirstSeen   time.Time
	LastSeen    time.Time
	Packets     uint64
	Bytes       uint64
	PacketsAToB uint64
	PacketsBToA uint64
}

// ObserveResult describes the registry mutation caused by a packet.
type ObserveResult struct {
	Flow             Snapshot
	PreviousLastSeen time.Time
	Created          bool
	Evicted          *Snapshot
}

// Registry retains a bounded, concurrency-safe set of recently observed flows.
type Registry struct {
	mu       sync.RWMutex
	seed     string
	capacity int
	ttl      time.Duration
	flows    map[string]*registryEntry
	oldest   entryHeap
}

type registryEntry struct {
	snapshot  Snapshot
	heapIndex int
}

// NewRegistry validates config and constructs an empty registry.
func NewRegistry(config RegistryConfig) (*Registry, error) {
	if config.Seed == "" {
		return nil, errors.New("flow registry seed is required")
	}
	if config.Capacity <= 0 {
		return nil, errors.New("flow registry capacity must be positive")
	}
	if config.TTL <= 0 {
		return nil, errors.New("flow registry TTL must be positive")
	}
	return &Registry{
		seed:     config.Seed,
		capacity: config.Capacity,
		ttl:      config.TTL,
		flows:    make(map[string]*registryEntry, config.Capacity),
	}, nil
}

// Observe validates and records a packet. Timestamp order is normalized so a
// late capture record cannot move a flow backward in time.
func (r *Registry) Observe(event packet.Event) (ObserveResult, error) {
	if err := event.Validate(); err != nil {
		return ObserveResult{}, err
	}
	if event.CapturedAt.IsZero() {
		return ObserveResult{}, errors.New("captured timestamp is required")
	}

	key, direction := Canonicalize(event)
	id := key.ID(r.seed)

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.flows[id]; ok {
		previousLastSeen := existing.snapshot.LastSeen
		r.update(existing, event, direction)
		return ObserveResult{Flow: existing.snapshot, PreviousLastSeen: previousLastSeen}, nil
	}

	var evicted *Snapshot
	if len(r.flows) >= r.capacity {
		oldest := heap.Pop(&r.oldest).(*registryEntry)
		delete(r.flows, oldest.snapshot.ID)
		copy := oldest.snapshot
		evicted = &copy
	}

	entry := &registryEntry{
		snapshot: Snapshot{
			ID:        id,
			Key:       key,
			FirstSeen: event.CapturedAt,
			LastSeen:  event.CapturedAt,
			Packets:   1,
			Bytes:     uint64(event.WireLength),
		},
		heapIndex: -1,
	}
	if direction == DirectionAToB {
		entry.snapshot.PacketsAToB = 1
	} else {
		entry.snapshot.PacketsBToA = 1
	}
	r.flows[id] = entry
	heap.Push(&r.oldest, entry)
	return ObserveResult{Flow: entry.snapshot, Created: true, Evicted: evicted}, nil
}

// Expire removes and returns flows whose last activity is older than the TTL.
func (r *Registry) Expire(now time.Time) []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := now.Add(-r.ttl)
	var expired []Snapshot
	for r.oldest.Len() > 0 {
		entry := r.oldest[0]
		if entry.snapshot.LastSeen.After(cutoff) {
			break
		}
		entry = heap.Pop(&r.oldest).(*registryEntry)
		delete(r.flows, entry.snapshot.ID)
		expired = append(expired, entry.snapshot)
	}
	return expired
}

// Get retrieves an immutable flow snapshot.
func (r *Registry) Get(id string) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.flows[id]
	if !ok {
		return Snapshot{}, false
	}
	return entry.snapshot, true
}

// Snapshots returns flows ordered by most recent activity, then ID.
func (r *Registry) Snapshots() []Snapshot {
	result, _ := r.RecentSnapshots(r.capacity)
	return result
}

// RecentSnapshots returns at most limit flows ordered by most recent activity,
// then ID, together with the total number of flows retained at the same
// snapshot boundary. It retains only the requested number of detached values
// while scanning the registry.
func (r *Registry) RecentSnapshots(limit int) ([]Snapshot, int) {
	r.mu.RLock()
	total := len(r.flows)
	if limit <= 0 || total == 0 {
		r.mu.RUnlock()
		return []Snapshot{}, total
	}
	if limit > total {
		limit = total
	}

	result := make(recentSnapshotHeap, 0, limit)
	for _, entry := range r.flows {
		candidate := entry.snapshot
		if len(result) < limit {
			heap.Push(&result, candidate)
			continue
		}
		if snapshotBefore(candidate, result[0]) {
			result[0] = candidate
			heap.Fix(&result, 0)
		}
	}
	r.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		return snapshotBefore(result[i], result[j])
	})
	return []Snapshot(result), total
}

// Len returns the current number of retained flows.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.flows)
}

func (r *Registry) update(entry *registryEntry, event packet.Event, direction Direction) {
	entry.snapshot.Packets++
	entry.snapshot.Bytes += uint64(event.WireLength)
	if direction == DirectionAToB {
		entry.snapshot.PacketsAToB++
	} else {
		entry.snapshot.PacketsBToA++
	}
	if event.CapturedAt.Before(entry.snapshot.FirstSeen) {
		entry.snapshot.FirstSeen = event.CapturedAt
	}
	if event.CapturedAt.After(entry.snapshot.LastSeen) {
		entry.snapshot.LastSeen = event.CapturedAt
		heap.Fix(&r.oldest, entry.heapIndex)
	}
}

type entryHeap []*registryEntry

// recentSnapshotHeap keeps the least recent retained candidate at its root so
// a registry scan can select the newest limit values without copying all flows.
type recentSnapshotHeap []Snapshot

func snapshotBefore(left, right Snapshot) bool {
	if left.LastSeen.Equal(right.LastSeen) {
		return left.ID < right.ID
	}
	return left.LastSeen.After(right.LastSeen)
}

func (h recentSnapshotHeap) Len() int { return len(h) }

func (h recentSnapshotHeap) Less(i, j int) bool {
	return snapshotBefore(h[j], h[i])
}

func (h recentSnapshotHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *recentSnapshotHeap) Push(value any) {
	*h = append(*h, value.(Snapshot))
}

func (h *recentSnapshotHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func (h entryHeap) Len() int { return len(h) }

func (h entryHeap) Less(i, j int) bool {
	if h[i].snapshot.LastSeen.Equal(h[j].snapshot.LastSeen) {
		return h[i].snapshot.ID < h[j].snapshot.ID
	}
	return h[i].snapshot.LastSeen.Before(h[j].snapshot.LastSeen)
}

func (h entryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *entryHeap) Push(value any) {
	entry := value.(*registryEntry)
	entry.heapIndex = len(*h)
	*h = append(*h, entry)
}

func (h *entryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.heapIndex = -1
	*h = old[:last]
	return entry
}
