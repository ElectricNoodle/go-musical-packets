package peer

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/coder/websocket"
)

// NoteSink accepts validated remote triggers. The receiving host remains the
// owner of scheduler admission and Note Off timing.
type NoteSink interface {
	Write(context.Context, music.NoteEvent) error
}

// HostConfig defines one authenticated inbound endpoint and bounded registry.
type HostConfig struct {
	Token              string
	InstanceID         string
	MappingVersion     string
	MaximumConnections int
	RecentCapacity     int
	RecentTTL          time.Duration
	StaleAfter         time.Duration
	DuplicateCapacity  int
	Sink               NoteSink
	Observer           Observer
	Now                func() time.Time
}

// Host accepts edge WebSockets and retains bounded current/recent state.
type Host struct {
	config HostConfig

	mu         sync.Mutex
	nodes      map[string]*hostNode
	generation uint64
	duplicates map[string]time.Time
	dupOrder   []duplicateEntry
}

type duplicateEntry struct {
	id   string
	time time.Time
}

type hostNode struct {
	snapshot   NodeSnapshot
	recent     []time.Time
	channels   [16]bool
	generation uint64
	connection *websocket.Conn
}

// NewHost validates and creates an HTTP peer endpoint.
func NewHost(config HostConfig) (*Host, error) {
	if len(config.Token) < 16 || !safeIdentifier(config.InstanceID, 128) || config.Sink == nil {
		return nil, errors.New("peer host token, instance ID, and note sink are required")
	}
	if config.MappingVersion != music.FlowModeV1 {
		return nil, errors.New("peer host mapping version is unsupported")
	}
	if config.MaximumConnections <= 0 || config.RecentCapacity < config.MaximumConnections || config.RecentTTL <= 0 || config.StaleAfter <= 0 || config.DuplicateCapacity <= 0 {
		return nil, errors.New("peer host connection, history, stale, or duplicate bounds are invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Observer == nil || nilObserver(config.Observer) {
		config.Observer = noopObserver{}
	}
	return &Host{
		config: config, nodes: make(map[string]*hostNode),
		duplicates: make(map[string]time.Time),
	}, nil
}

// ServeHTTP authenticates before upgrading and only accepts native clients
// without a browser Origin header.
func (host *Host) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(request.Header.Values("Origin")) != 0 {
		http.Error(response, "peer WebSocket does not accept browser origins", http.StatusForbidden)
		return
	}
	if !validBearer(request.Header.Get("Authorization"), host.config.Token) {
		response.Header().Set("WWW-Authenticate", `Bearer realm="musical-packets-peer"`)
		http.Error(response, "peer authentication failed", http.StatusUnauthorized)
		return
	}
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(MaximumFrame)

	ctx := request.Context()
	handshakeContext, cancel := context.WithTimeout(ctx, handshakeTimeout)
	message, err := readMessage(handshakeContext, connection)
	cancel()
	if err != nil || message.Type != TypeHello || message.Hello.Role != "edge" {
		host.sendProtocolError(ctx, connection, "invalid_hello", "a valid edge hello is required")
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid peer hello")
		return
	}
	hello := *message.Hello
	if hello.MappingVersion != host.config.MappingVersion {
		host.sendProtocolError(ctx, connection, "mapping_version", "the mapping version is not supported")
		_ = connection.Close(websocket.StatusPolicyViolation, "unsupported mapping version")
		return
	}

	generation, replaced, ok := host.register(hello, request.RemoteAddr, connection)
	if !ok {
		host.sendProtocolError(ctx, connection, "capacity", "peer connection capacity reached")
		_ = connection.Close(websocket.StatusTryAgainLater, "peer capacity reached")
		return
	}
	if replaced != nil {
		replaced.CloseNow()
	}
	defer host.disconnect(hello.InstanceID, generation)
	if replaced == nil {
		host.config.Observer.Connection("inbound", "connected")
	}

	if err := writeMessage(ctx, connection, Message{
		Type: TypeHello, Version: ProtocolVersion,
		Hello: &Hello{InstanceID: host.config.InstanceID, Role: "host", MappingVersion: host.config.MappingVersion},
	}); err != nil {
		return
	}

	for {
		message, err := readMessage(ctx, connection)
		if err != nil {
			return
		}
		switch message.Type {
		case TypeNote:
			if err := host.acceptNote(ctx, hello.InstanceID, generation, *message.Note); err != nil {
				host.sendProtocolError(ctx, connection, "invalid_note", safeError(err))
			}
		case TypePing:
			host.touch(hello.InstanceID, generation)
			if err := writeMessage(ctx, connection, Message{Type: TypePong, Version: ProtocolVersion, Pong: message.Ping}); err != nil {
				return
			}
		case TypePong:
			host.touch(hello.InstanceID, generation)
		default:
			host.sendProtocolError(ctx, connection, "unexpected_message", "the message is not valid after handshake")
			_ = connection.Close(websocket.StatusPolicyViolation, "unexpected peer message")
			return
		}
	}
}

func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || len(header) != len(prefix)+len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(token)) == 1
}

func (host *Host) register(hello Hello, remote string, connection *websocket.Conn) (uint64, *websocket.Conn, bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	now := host.config.Now().UTC()
	host.pruneNodesLocked(now)
	existing := host.nodes[hello.InstanceID]
	connected := 0
	for id, node := range host.nodes {
		if id != hello.InstanceID && node.snapshot.State == "connected" {
			connected++
		}
	}
	if connected >= host.config.MaximumConnections {
		return 0, nil, false
	}
	host.generation++
	node := &hostNode{
		snapshot: NodeSnapshot{
			InstanceID: hello.InstanceID, RemoteAddress: boundedRemote(remote), State: "connected",
			Authenticated: true, ProtocolVersion: ProtocolVersion, MappingVersion: hello.MappingVersion,
			ConnectedAt: now, LastSeenAt: now,
		},
		generation: host.generation, connection: connection,
	}
	host.nodes[hello.InstanceID] = node
	host.pruneNodesLocked(now)
	var replaced *websocket.Conn
	if existing != nil && existing.snapshot.State == "connected" {
		replaced = existing.connection
	}
	return node.generation, replaced, true
}

func boundedRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if len(remote) > 128 {
		return remote[:128]
	}
	return remote
}

func (host *Host) disconnect(instanceID string, generation uint64) {
	disconnected := false
	host.mu.Lock()
	node := host.nodes[instanceID]
	if node != nil && node.generation == generation {
		now := host.config.Now().UTC()
		node.snapshot.State = "disconnected"
		node.snapshot.DisconnectedAt = now
		node.connection = nil
		disconnected = true
	}
	host.mu.Unlock()
	if disconnected {
		host.config.Observer.Connection("inbound", "disconnected")
	}
}

func (host *Host) touch(instanceID string, generation uint64) {
	host.mu.Lock()
	if node := host.nodes[instanceID]; node != nil && node.generation == generation {
		node.snapshot.LastSeenAt = host.config.Now().UTC()
	}
	host.mu.Unlock()
}

func (host *Host) acceptNote(ctx context.Context, instanceID string, generation uint64, note Note) error {
	event, err := note.Event()
	if err != nil {
		host.record(instanceID, generation, "rejected", 0)
		host.config.Observer.Event("inbound", "rejected")
		return err
	}
	if event.Origin != instanceID {
		host.record(instanceID, generation, "rejected", 0)
		host.config.Observer.Event("inbound", "rejected")
		return errors.New("note origin does not match authenticated edge")
	}
	now := host.config.Now().UTC()
	age := now.Sub(event.CreatedAt)
	if age > host.config.StaleAfter {
		host.record(instanceID, generation, "stale", event.Channel)
		host.config.Observer.Event("inbound", "dropped_stale")
		return errors.New("note is stale")
	}
	if age < -host.config.StaleAfter {
		host.record(instanceID, generation, "rejected", event.Channel)
		host.config.Observer.Event("inbound", "rejected")
		return errors.New("note creation time is too far in the future")
	}
	if host.duplicate(event.Origin+"\x00"+event.ID, now) {
		host.record(instanceID, generation, "duplicate", event.Channel)
		host.config.Observer.Event("inbound", "duplicate")
		return errors.New("note is a duplicate")
	}
	if err := host.config.Sink.Write(ctx, event); err != nil {
		host.record(instanceID, generation, "rejected", event.Channel)
		host.config.Observer.Event("inbound", "rejected")
		return fmt.Errorf("host scheduler rejected note: %w", err)
	}
	host.record(instanceID, generation, "accepted", event.Channel)
	host.config.Observer.Event("inbound", "accepted")
	return nil
}

func (host *Host) duplicate(id string, now time.Time) bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.pruneDuplicatesLocked(now)
	if _, exists := host.duplicates[id]; exists {
		return true
	}
	host.duplicates[id] = now
	host.dupOrder = append(host.dupOrder, duplicateEntry{id: id, time: now})
	for len(host.dupOrder) > host.config.DuplicateCapacity {
		entry := host.dupOrder[0]
		host.dupOrder = host.dupOrder[1:]
		if recorded, exists := host.duplicates[entry.id]; exists && recorded.Equal(entry.time) {
			delete(host.duplicates, entry.id)
		}
	}
	return false
}

func (host *Host) pruneDuplicatesLocked(now time.Time) {
	cutoff := now.Add(-host.config.RecentTTL)
	for len(host.dupOrder) > 0 && host.dupOrder[0].time.Before(cutoff) {
		entry := host.dupOrder[0]
		host.dupOrder = host.dupOrder[1:]
		if recorded, exists := host.duplicates[entry.id]; exists && recorded.Equal(entry.time) {
			delete(host.duplicates, entry.id)
		}
	}
}

func (host *Host) record(instanceID string, generation uint64, result string, channel uint8) {
	host.mu.Lock()
	defer host.mu.Unlock()
	node := host.nodes[instanceID]
	if node == nil || node.generation != generation {
		return
	}
	now := host.config.Now().UTC()
	node.snapshot.LastSeenAt = now
	node.snapshot.ReceivedTotal++
	if channel >= 1 && channel <= 16 {
		node.channels[channel-1] = true
	}
	switch result {
	case "accepted":
		node.snapshot.AcceptedTotal++
		node.recent = appendRateSample(node.recent, now)
	case "duplicate":
		node.snapshot.DuplicateTotal++
	case "stale":
		node.snapshot.StaleTotal++
	default:
		node.snapshot.RejectedTotal++
	}
}

func (host *Host) sendProtocolError(ctx context.Context, connection *websocket.Conn, code, detail string) {
	_ = writeMessage(ctx, connection, Message{
		Type: TypeError, Version: ProtocolVersion,
		Error: &ProtocolError{Code: code, Detail: detail},
	})
}

// Snapshot returns connected nodes followed by bounded recent disconnects.
func (host *Host) Snapshot() Snapshot {
	host.mu.Lock()
	defer host.mu.Unlock()
	now := host.config.Now().UTC()
	host.pruneNodesLocked(now)
	nodes := make([]NodeSnapshot, 0, len(host.nodes))
	for _, node := range host.nodes {
		cutoff := now.Add(-rateWindow)
		index := sort.Search(len(node.recent), func(index int) bool { return !node.recent[index].Before(cutoff) })
		if index > 0 {
			copy(node.recent, node.recent[index:])
			node.recent = node.recent[:len(node.recent)-index]
		}
		copy := node.snapshot
		copy.NoteRate = float64(len(node.recent)) / rateWindow.Seconds()
		copy.ActiveChannels = channels(node.channels)
		nodes = append(nodes, copy)
	}
	sortNodes(nodes)
	return Snapshot{Role: "host", Nodes: nodes}
}

func (host *Host) pruneNodesLocked(now time.Time) {
	cutoff := now.Add(-host.config.RecentTTL)
	for id, node := range host.nodes {
		if node.snapshot.State == "disconnected" && node.snapshot.DisconnectedAt.Before(cutoff) {
			delete(host.nodes, id)
		}
	}
	if len(host.nodes) <= host.config.RecentCapacity {
		return
	}
	type candidate struct {
		id   string
		time time.Time
	}
	recent := make([]candidate, 0, len(host.nodes))
	for id, node := range host.nodes {
		if node.snapshot.State == "disconnected" {
			recent = append(recent, candidate{id: id, time: node.snapshot.DisconnectedAt})
		}
	}
	sort.Slice(recent, func(left, right int) bool { return recent[left].time.Before(recent[right].time) })
	for _, candidate := range recent {
		if len(host.nodes) <= host.config.RecentCapacity {
			break
		}
		delete(host.nodes, candidate.id)
	}
}
