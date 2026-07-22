package peer

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/coder/websocket"
)

const (
	handshakeTimeout  = 5 * time.Second
	heartbeatInterval = 10 * time.Second
)

var ErrQueueFull = errors.New("peer outgoing queue is full")

// Observer records peer transport activity with bounded label values.
type Observer interface {
	Connection(direction, state string)
	Event(direction, result string)
	Queue(depth, capacity int)
	RoundTrip(time.Duration)
}

type noopObserver struct{}

func (noopObserver) Connection(string, string) {}
func (noopObserver) Event(string, string)      {}
func (noopObserver) Queue(int, int)            {}
func (noopObserver) RoundTrip(time.Duration)   {}

// EdgeConfig defines one reconnecting, bounded outbound peer.
type EdgeConfig struct {
	URL            string
	Token          string
	InstanceID     string
	MappingVersion string
	QueueCapacity  int
	StaleAfter     time.Duration
	ReconnectBase  time.Duration
	ReconnectLimit time.Duration
	HTTPClient     *http.Client
	Observer       Observer
	Now            func() time.Time
	Jitter         func(time.Duration) time.Duration
}

// Edge is both a non-blocking pipeline sink and a reconnecting transport.
type Edge struct {
	config EdgeConfig
	queue  chan music.NoteEvent
	state  *outboundState
	nonce  atomic.Uint64
}

// NewEdge validates and creates a disconnected sender.
func NewEdge(config EdgeConfig) (*Edge, error) {
	if config.URL == "" || len(config.Token) < 16 || !safeIdentifier(config.InstanceID, 128) {
		return nil, errors.New("peer edge URL, token, and instance ID are required")
	}
	parsedURL, err := url.Parse(config.URL)
	if err != nil || parsedURL.Host == "" || parsedURL.User != nil || (parsedURL.Scheme != "ws" && parsedURL.Scheme != "wss") {
		return nil, errors.New("peer edge URL must be absolute ws:// or wss:// without user information")
	}
	if config.MappingVersion != music.FlowModeV1 {
		return nil, errors.New("peer edge mapping version is unsupported")
	}
	if config.QueueCapacity <= 0 || config.StaleAfter <= 0 || config.ReconnectBase <= 0 || config.ReconnectLimit < config.ReconnectBase {
		return nil, errors.New("peer edge queue, stale, and reconnect settings are invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Jitter == nil {
		config.Jitter = func(duration time.Duration) time.Duration {
			return time.Duration(float64(duration) * (0.8 + rand.Float64()*0.4))
		}
	}
	if config.Observer == nil || nilObserver(config.Observer) {
		config.Observer = noopObserver{}
	}
	edge := &Edge{
		config: config,
		queue:  make(chan music.NoteEvent, config.QueueCapacity),
		state:  newOutboundState(config.URL, config.QueueCapacity),
	}
	config.Observer.Queue(0, config.QueueCapacity)
	return edge, nil
}

func nilObserver(observer Observer) bool {
	value := reflect.ValueOf(observer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Write enqueues one note without allowing network backpressure into capture.
func (edge *Edge) Write(ctx context.Context, event music.NoteEvent) error {
	if ctx == nil {
		return errors.New("peer edge write context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	select {
	case edge.queue <- event:
		edge.config.Observer.Event("outbound", "queued")
		edge.config.Observer.Queue(len(edge.queue), cap(edge.queue))
		return nil
	default:
		edge.state.mu.Lock()
		edge.state.snapshot.DroppedFull++
		edge.state.mu.Unlock()
		edge.config.Observer.Event("outbound", "dropped_full")
		return ErrQueueFull
	}
}

// Run reconnects until cancellation. Recoverable network failures are exposed
// through Snapshot rather than terminating the application.
func (edge *Edge) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("peer edge run context is required")
	}
	backoff := edge.config.ReconnectBase
	for ctx.Err() == nil {
		now := edge.config.Now().UTC()
		edge.state.mu.Lock()
		edge.state.snapshot.State = "connecting"
		edge.state.snapshot.LastAttemptAt = now
		edge.state.snapshot.NextRetryAt = time.Time{}
		edge.state.mu.Unlock()
		edge.config.Observer.Connection("outbound", "connecting")

		connected, err := edge.runSession(ctx)
		if ctx.Err() != nil {
			break
		}
		if connected {
			backoff = edge.config.ReconnectBase
		}
		edge.state.mu.Lock()
		edge.state.snapshot.State = "backoff"
		edge.state.snapshot.LastError = edge.safeError(err)
		edge.state.snapshot.Reconnects++
		delay := edge.config.Jitter(backoff)
		if delay <= 0 {
			delay = backoff
		}
		edge.state.snapshot.NextRetryAt = edge.config.Now().UTC().Add(delay)
		edge.state.mu.Unlock()
		edge.config.Observer.Connection("outbound", "backoff")

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > edge.config.ReconnectLimit {
			backoff = edge.config.ReconnectLimit
		}
	}
	edge.state.mu.Lock()
	edge.state.snapshot.State = "disconnected"
	edge.state.snapshot.NextRetryAt = time.Time{}
	edge.state.mu.Unlock()
	edge.config.Observer.Connection("outbound", "disconnected")
	return nil
}

func (edge *Edge) runSession(ctx context.Context) (bool, error) {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+edge.config.Token)
	connection, _, err := websocket.Dial(ctx, edge.config.URL, &websocket.DialOptions{
		HTTPClient: edge.config.HTTPClient, HTTPHeader: headers,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return false, fmt.Errorf("connect to peer host: %w", err)
	}
	defer connection.CloseNow()
	connection.SetReadLimit(MaximumFrame)

	handshakeContext, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := writeMessage(handshakeContext, connection, Message{
		Type: TypeHello, Version: ProtocolVersion,
		Hello: &Hello{InstanceID: edge.config.InstanceID, Role: "edge", MappingVersion: edge.config.MappingVersion},
	}); err != nil {
		return false, fmt.Errorf("send peer hello: %w", err)
	}
	reply, err := readMessage(handshakeContext, connection)
	if err != nil {
		return false, fmt.Errorf("read peer hello: %w", err)
	}
	if reply.Type != TypeHello || reply.Hello.Role != "host" {
		return false, errors.New("peer host did not return a host hello")
	}
	if reply.Hello.InstanceID == edge.config.InstanceID {
		return false, errors.New("peer host instance ID must differ from the edge")
	}

	connectedAt := edge.config.Now().UTC()
	edge.state.mu.Lock()
	edge.state.snapshot.State = "connected"
	edge.state.snapshot.RemoteInstance = reply.Hello.InstanceID
	edge.state.snapshot.ProtocolVersion = ProtocolVersion
	edge.state.snapshot.MappingVersion = reply.Hello.MappingVersion
	edge.state.snapshot.ConnectedAt = connectedAt
	edge.state.snapshot.LastError = ""
	edge.state.snapshot.NextRetryAt = time.Time{}
	edge.state.mu.Unlock()
	edge.config.Observer.Connection("outbound", "connected")

	incoming := make(chan Message, 1)
	readErrors := make(chan error, 1)
	go func() {
		for {
			message, readErr := readMessage(ctx, connection)
			if readErr != nil {
				readErrors <- readErr
				return
			}
			select {
			case incoming <- message:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	var pendingNonce uint64
	var pendingSent time.Time
	for {
		select {
		case <-ctx.Done():
			_ = connection.Close(websocket.StatusNormalClosure, "edge shutting down")
			return true, nil
		case err := <-readErrors:
			return true, fmt.Errorf("read peer host: %w", err)
		case message := <-incoming:
			switch message.Type {
			case TypePong:
				if message.Pong.Nonce == pendingNonce && !pendingSent.IsZero() {
					rtt := edge.config.Now().Sub(pendingSent)
					if rtt >= 0 {
						edge.state.mu.Lock()
						edge.state.snapshot.RTT = rtt
						edge.state.mu.Unlock()
						edge.config.Observer.RoundTrip(rtt)
					}
					pendingNonce = 0
				}
			case TypePing:
				if err := writeMessage(ctx, connection, Message{Type: TypePong, Version: ProtocolVersion, Pong: message.Ping}); err != nil {
					return true, err
				}
			case TypeError:
				edge.state.mu.Lock()
				edge.state.snapshot.LastError = message.Error.Code + ": " + message.Error.Detail
				edge.state.mu.Unlock()
				edge.config.Observer.Event("outbound", "rejected")
			default:
				return true, fmt.Errorf("peer host sent unexpected %s message", message.Type)
			}
		case now := <-ticker.C:
			if pendingNonce != 0 {
				return true, errors.New("peer heartbeat timed out")
			}
			nonce := edge.nonce.Add(1)
			pendingNonce, pendingSent = nonce, now.UTC()
			if err := writeMessage(ctx, connection, Message{Type: TypePing, Version: ProtocolVersion, Ping: &Heartbeat{Nonce: nonce, SentAt: pendingSent}}); err != nil {
				return true, err
			}
		case event := <-edge.queue:
			edge.config.Observer.Queue(len(edge.queue), cap(edge.queue))
			now := edge.config.Now().UTC()
			if event.CreatedAt.IsZero() || now.Sub(event.CreatedAt) > edge.config.StaleAfter {
				edge.state.mu.Lock()
				edge.state.snapshot.DroppedStale++
				edge.state.mu.Unlock()
				edge.config.Observer.Event("outbound", "dropped_stale")
				continue
			}
			note, err := NoteFromEvent(event)
			if err != nil {
				edge.config.Observer.Event("outbound", "invalid")
				continue
			}
			if err := writeMessage(ctx, connection, Message{Type: TypeNote, Version: ProtocolVersion, Note: &note}); err != nil {
				edge.config.Observer.Event("outbound", "failed")
				return true, err
			}
			edge.state.mu.Lock()
			edge.state.snapshot.SentTotal++
			edge.state.snapshot.LastSentAt = now
			edge.state.recent = appendRateSample(edge.state.recent, now)
			edge.state.channels[event.Channel-1] = true
			edge.state.mu.Unlock()
			edge.config.Observer.Event("outbound", "sent")
		}
	}
}

func (edge *Edge) safeError(err error) string {
	message := safeError(err)
	message = strings.ReplaceAll(message, edge.config.URL, safeTarget(edge.config.URL))
	message = strings.ReplaceAll(message, edge.config.Token, "<redacted>")
	return message
}

// Snapshot returns a detached operational view.
func (edge *Edge) Snapshot() Snapshot {
	copy := edge.state.copy(edge.config.Now().UTC(), len(edge.queue))
	return Snapshot{Role: "edge", Enabled: true, Outbound: &copy, Nodes: []NodeSnapshot{}}
}

func writeMessage(ctx context.Context, connection *websocket.Conn, message Message) error {
	encoded, err := Encode(message)
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, encoded)
}

func readMessage(ctx context.Context, connection *websocket.Conn) (Message, error) {
	kind, encoded, err := connection.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	if kind != websocket.MessageText {
		return Message{}, errors.New("peer frame must be text")
	}
	return Decode(encoded)
}
