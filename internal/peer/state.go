package peer

import (
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const rateWindow = 10 * time.Second
const maximumRateSamples = 4096

// Snapshot is a detached role-aware view suitable for a local management API.
type Snapshot struct {
	Role     string
	Enabled  bool
	Outbound *OutboundSnapshot
	Nodes    []NodeSnapshot
}

// OutboundSnapshot describes one edge's configured host connection.
type OutboundSnapshot struct {
	Enabled         bool
	Target          string
	RemoteInstance  string
	State           string
	ProtocolVersion string
	MappingVersion  string
	QueueDepth      int
	QueueCapacity   int
	SentTotal       uint64
	DroppedFull     uint64
	DroppedStale    uint64
	Reconnects      uint64
	SendRate        float64
	LastSentAt      time.Time
	ConnectedAt     time.Time
	LastAttemptAt   time.Time
	NextRetryAt     time.Time
	RTT             time.Duration
	LastError       string
	ActiveChannels  []uint8
}

// NodeSnapshot describes one current or recently disconnected edge on a host.
type NodeSnapshot struct {
	InstanceID      string
	RemoteAddress   string
	State           string
	Authenticated   bool
	ProtocolVersion string
	MappingVersion  string
	ConnectedAt     time.Time
	DisconnectedAt  time.Time
	LastSeenAt      time.Time
	NoteRate        float64
	ReceivedTotal   uint64
	AcceptedTotal   uint64
	RejectedTotal   uint64
	DuplicateTotal  uint64
	StaleTotal      uint64
	ActiveChannels  []uint8
}

type outboundState struct {
	mu       sync.Mutex
	snapshot OutboundSnapshot
	recent   []time.Time
	channels [16]bool
}

func newOutboundState(target string, capacity int) *outboundState {
	return &outboundState{snapshot: OutboundSnapshot{
		Enabled: true, Target: safeTarget(target), State: "disconnected",
		QueueCapacity: capacity, MappingVersion: "flow-mode-v1",
	}}
}

func (state *outboundState) copy(now time.Time, depth int) OutboundSnapshot {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.prune(now)
	copy := state.snapshot
	copy.QueueDepth = depth
	copy.SendRate = float64(len(state.recent)) / rateWindow.Seconds()
	copy.ActiveChannels = channels(state.channels)
	return copy
}

func (state *outboundState) prune(now time.Time) {
	cutoff := now.Add(-rateWindow)
	index := 0
	for index < len(state.recent) && state.recent[index].Before(cutoff) {
		index++
	}
	if index > 0 {
		copy(state.recent, state.recent[index:])
		state.recent = state.recent[:len(state.recent)-index]
	}
}

func safeTarget(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "configured peer"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 256 {
		message = message[:256]
	}
	return message
}

func appendRateSample(samples []time.Time, value time.Time) []time.Time {
	if len(samples) == maximumRateSamples {
		copy(samples, samples[1:])
		samples = samples[:maximumRateSamples-1]
	}
	return append(samples, value)
}

func channels(values [16]bool) []uint8 {
	result := make([]uint8, 0, 16)
	for index, active := range values {
		if active {
			result = append(result, uint8(index+1))
		}
	}
	return result
}

func sortNodes(nodes []NodeSnapshot) {
	sort.Slice(nodes, func(left, right int) bool {
		if nodes[left].State != nodes[right].State {
			return nodes[left].State == "connected"
		}
		return nodes[left].InstanceID < nodes[right].InstanceID
	})
}
