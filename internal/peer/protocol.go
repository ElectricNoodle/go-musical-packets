// Package peer implements the authenticated, bounded edge-to-host note protocol.
package peer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

const (
	ProtocolVersion = "peer-v1"
	Path            = "/api/v1/peer"
	MaximumFrame    = 16 << 10
	maximumDepth    = 16
)

const (
	TypeHello = "hello"
	TypeNote  = "note"
	TypePing  = "ping"
	TypePong  = "pong"
	TypeError = "error"
)

// Message is the strict versioned peer envelope. Exactly one payload matching
// Type is required.
type Message struct {
	Type    string         `json:"type"`
	Version string         `json:"version"`
	Hello   *Hello         `json:"hello,omitempty"`
	Note    *Note          `json:"note,omitempty"`
	Ping    *Heartbeat     `json:"ping,omitempty"`
	Pong    *Heartbeat     `json:"pong,omitempty"`
	Error   *ProtocolError `json:"error,omitempty"`
}

// Hello identifies one authenticated endpoint after the HTTP upgrade.
type Hello struct {
	InstanceID     string `json:"instance_id"`
	Role           string `json:"role"`
	MappingVersion string `json:"mapping_version"`
}

// Note is the stable JSON form of a transport-independent musical trigger.
type Note struct {
	ID             string    `json:"id"`
	Origin         string    `json:"origin"`
	Sequence       uint64    `json:"sequence"`
	MappingVersion string    `json:"mapping_version"`
	FlowID         string    `json:"flow_id"`
	Mode           string    `json:"mode"`
	Root           uint8     `json:"root"`
	Pitch          uint8     `json:"note"`
	Velocity       uint8     `json:"velocity"`
	DurationMS     int64     `json:"duration_ms"`
	Channel        uint8     `json:"channel"`
	CreatedAt      time.Time `json:"created_at"`
}

// Heartbeat measures application-level liveness and round-trip time.
type Heartbeat struct {
	Nonce  uint64    `json:"nonce"`
	SentAt time.Time `json:"sent_at"`
}

// ProtocolError is safe to expose to the authenticated peer.
type ProtocolError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Encode validates and serializes one peer message.
func Encode(message Message) ([]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode peer message: %w", err)
	}
	if len(encoded) > MaximumFrame {
		return nil, errors.New("encode peer message: frame exceeds maximum size")
	}
	return encoded, nil
}

// Decode performs strict schema, duplicate-name, depth, and semantic checks.
func Decode(encoded []byte) (Message, error) {
	if len(encoded) == 0 || len(encoded) > MaximumFrame {
		return Message{}, errors.New("decode peer message: frame size is invalid")
	}
	if !utf8.Valid(encoded) {
		return Message{}, errors.New("decode peer message: frame is not valid UTF-8")
	}
	if err := validateJSONNames(encoded); err != nil {
		return Message{}, fmt.Errorf("decode peer message: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var message Message
	if err := decoder.Decode(&message); err != nil {
		return Message{}, fmt.Errorf("decode peer message: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Message{}, fmt.Errorf("decode peer message: %w", err)
	}
	if err := message.Validate(); err != nil {
		return Message{}, fmt.Errorf("decode peer message: %w", err)
	}
	return message, nil
}

// Validate checks the envelope and its selected payload.
func (message Message) Validate() error {
	if message.Version != ProtocolVersion {
		return fmt.Errorf("protocol version %q is unsupported", message.Version)
	}
	payloads := 0
	for _, present := range []bool{message.Hello != nil, message.Note != nil, message.Ping != nil, message.Pong != nil, message.Error != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return errors.New("message must contain exactly one payload")
	}
	switch message.Type {
	case TypeHello:
		if message.Hello == nil {
			return errors.New("hello payload is required")
		}
		return message.Hello.validate()
	case TypeNote:
		if message.Note == nil {
			return errors.New("note payload is required")
		}
		_, err := message.Note.Event()
		return err
	case TypePing:
		if message.Ping == nil {
			return errors.New("ping payload is required")
		}
		return message.Ping.validate()
	case TypePong:
		if message.Pong == nil {
			return errors.New("pong payload is required")
		}
		return message.Pong.validate()
	case TypeError:
		if message.Error == nil || !safeIdentifier(message.Error.Code, 64) || strings.TrimSpace(message.Error.Detail) == "" || len(message.Error.Detail) > 256 {
			return errors.New("protocol error payload is invalid")
		}
		return nil
	default:
		return fmt.Errorf("message type %q is unsupported", message.Type)
	}
}

func (hello Hello) validate() error {
	if !safeIdentifier(hello.InstanceID, 128) {
		return errors.New("hello instance_id is invalid")
	}
	if hello.Role != "edge" && hello.Role != "host" {
		return errors.New("hello role must be edge or host")
	}
	if hello.MappingVersion != music.FlowModeV1 {
		return fmt.Errorf("mapping version %q is unsupported", hello.MappingVersion)
	}
	return nil
}

func (heartbeat Heartbeat) validate() error {
	if heartbeat.Nonce == 0 || heartbeat.SentAt.IsZero() {
		return errors.New("heartbeat nonce and sent_at are required")
	}
	return nil
}

// NoteFromEvent converts one validated domain event to its wire form.
func NoteFromEvent(event music.NoteEvent) (Note, error) {
	if err := validateEvent(event); err != nil {
		return Note{}, err
	}
	return Note{
		ID: event.ID, Origin: event.Origin, Sequence: event.Sequence,
		MappingVersion: event.MappingVersion, FlowID: event.FlowID,
		Mode: event.Mode.String(), Root: event.Root, Pitch: event.Note,
		Velocity: event.Velocity, DurationMS: event.Duration.Milliseconds(),
		Channel: event.Channel, CreatedAt: event.CreatedAt.UTC(),
	}, nil
}

// Event converts and validates a wire note.
func (note Note) Event() (music.NoteEvent, error) {
	if note.DurationMS <= 0 || note.DurationMS > math.MaxInt64/int64(time.Millisecond) {
		return music.NoteEvent{}, errors.New("note duration_ms is invalid")
	}
	mode, err := music.ParseMode(note.Mode)
	if err != nil {
		return music.NoteEvent{}, err
	}
	event := music.NoteEvent{
		ID: note.ID, Origin: note.Origin, Sequence: note.Sequence,
		MappingVersion: note.MappingVersion, FlowID: note.FlowID,
		Mode: mode, Root: note.Root, Note: note.Pitch, Velocity: note.Velocity,
		Duration: time.Duration(note.DurationMS) * time.Millisecond,
		Channel:  note.Channel, CreatedAt: note.CreatedAt.UTC(),
	}
	if err := validateEvent(event); err != nil {
		return music.NoteEvent{}, err
	}
	return event, nil
}

func validateEvent(event music.NoteEvent) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("note event: %w", err)
	}
	if !safeIdentifier(event.ID, 256) || !safeIdentifier(event.Origin, 128) {
		return errors.New("note event ID or origin is invalid")
	}
	if len(event.FlowID) != 24 || strings.Trim(event.FlowID, "0123456789abcdef") != "" {
		return errors.New("note flow_id is invalid")
	}
	if event.MappingVersion != music.FlowModeV1 {
		return fmt.Errorf("note mapping version %q is unsupported", event.MappingVersion)
	}
	if event.Sequence == 0 {
		return errors.New("note sequence must be positive")
	}
	if event.CreatedAt.IsZero() || event.Duration%time.Millisecond != 0 || event.Duration.Milliseconds() <= 0 {
		return errors.New("note timing is invalid")
	}
	return nil
}

func safeIdentifier(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum &&
		!strings.ContainsAny(value, "\x00\r\n") && strings.IndexFunc(value, unicode.IsControl) == -1
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("frame contains trailing JSON")
		}
		return err
	}
	return nil
}

func validateJSONNames(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := walkJSON(decoder, 0); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func walkJSON(decoder *json.Decoder, depth int) error {
	if depth > maximumDepth {
		return errors.New("JSON nesting exceeds maximum depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("object name is invalid")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate object name %q", name)
			}
			seen[name] = struct{}{}
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	_, err = decoder.Token()
	return err
}
