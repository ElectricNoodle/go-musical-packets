// Package uistream publishes scheduler-accepted notes to local browser clients
// without placing browser backpressure on capture or MIDI.
package uistream

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

// Observer receives bounded-cardinality stream lifecycle events.
type Observer interface {
	Clients(int)
	Events(string, int)
}

type noopObserver struct{}

func (noopObserver) Clients(int)        {}
func (noopObserver) Events(string, int) {}

// Note is the stable JSON representation consumed by the browser viewer.
type Note struct {
	ID             string    `json:"id"`
	Origin         string    `json:"origin"`
	Sequence       uint64    `json:"sequence"`
	MappingVersion string    `json:"mapping_version"`
	FlowID         string    `json:"flow_id"`
	Mode           string    `json:"mode"`
	Root           uint8     `json:"root"`
	Note           uint8     `json:"note"`
	Velocity       uint8     `json:"velocity"`
	DurationMS     int64     `json:"duration_ms"`
	Channel        uint8     `json:"channel"`
	CreatedAt      time.Time `json:"created_at"`
	AcceptedAt     time.Time `json:"accepted_at"`
}

// Batch aggregates live notes to a bounded update frequency.
type Batch struct {
	Type        string    `json:"type"`
	SentAt      time.Time `json:"sent_at"`
	Dropped     uint64    `json:"dropped"`
	PacketTotal uint64    `json:"packet_total"`
	NoteTotal   uint64    `json:"note_total"`
	Notes       []Note    `json:"notes"`
}

// Hub fans accepted notes out to fixed-capacity subscriber queues.
type Hub struct {
	mu       sync.RWMutex
	capacity int
	observer Observer
	nextID   uint64
	clients  map[uint64]*Subscription
	packets  atomic.Uint64
	notes    atomic.Uint64
}

// Subscription is one browser's bounded event queue.
type Subscription struct {
	hub    *Hub
	id     uint64
	queue  chan Note
	drops  atomic.Uint64
	closed atomic.Bool
}

// New constructs an empty bounded stream hub.
func New(capacity int, observer Observer) *Hub {
	if observer == nil {
		observer = noopObserver{}
	}
	return &Hub{capacity: capacity, observer: observer, clients: make(map[uint64]*Subscription)}
}

// Subscribe registers one independently bounded browser consumer.
func (hub *Hub) Subscribe() *Subscription {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.nextID++
	subscription := &Subscription{hub: hub, id: hub.nextID, queue: make(chan Note, hub.capacity)}
	hub.clients[subscription.id] = subscription
	hub.observer.Clients(len(hub.clients))
	return subscription
}

// Publish makes one accepted note available without blocking the caller. When
// a browser is slow, its oldest queued note is discarded in favor of recency.
func (hub *Hub) Publish(event music.NoteEvent) {
	hub.notes.Add(1)
	note := noteFromEvent(event, time.Now().UTC())
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for _, subscription := range hub.clients {
		select {
		case subscription.queue <- note:
		default:
			select {
			case <-subscription.queue:
				subscription.drops.Add(1)
				hub.observer.Events("dropped", 1)
			default:
			}
			select {
			case subscription.queue <- note:
			default:
				subscription.drops.Add(1)
				hub.observer.Events("dropped", 1)
			}
		}
	}
}

// RecordPacket advances the cumulative capture counter exposed in batches.
func (hub *Hub) RecordPacket() { hub.packets.Add(1) }

func (hub *Hub) totals() (uint64, uint64) { return hub.packets.Load(), hub.notes.Load() }

// Events returns the subscription's receive-only queue.
func (subscription *Subscription) Events() <-chan Note { return subscription.queue }

// TakeDrops atomically returns drops since the previous call.
func (subscription *Subscription) TakeDrops() uint64 { return subscription.drops.Swap(0) }

// Close unregisters the subscription. The queue remains safe for any in-flight
// publisher and becomes unreachable once its handler returns.
func (subscription *Subscription) Close() {
	if !subscription.closed.CompareAndSwap(false, true) {
		return
	}
	hub := subscription.hub
	hub.mu.Lock()
	delete(hub.clients, subscription.id)
	hub.observer.Clients(len(hub.clients))
	hub.mu.Unlock()
}

func noteFromEvent(event music.NoteEvent, acceptedAt time.Time) Note {
	return Note{
		ID:             event.ID,
		Origin:         event.Origin,
		Sequence:       event.Sequence,
		MappingVersion: event.MappingVersion,
		FlowID:         event.FlowID,
		Mode:           event.Mode.String(),
		Root:           event.Root,
		Note:           event.Note,
		Velocity:       event.Velocity,
		DurationMS:     event.Duration.Milliseconds(),
		Channel:        event.Channel,
		CreatedAt:      event.CreatedAt,
		AcceptedAt:     acceptedAt,
	}
}
