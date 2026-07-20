package music

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

const FlowModeV1 = "flow-mode-v1"

// MapperConfig contains the pure flow-mode-v1 mapping controls.
type MapperConfig struct {
	Seed            string
	Origin          string
	MinimumNote     uint8
	MaximumNote     uint8
	MinimumDuration time.Duration
	MaximumDuration time.Duration
}

// MapInput supplies packet metadata plus routing state selected outside the
// mapper. InterArrival is measured within the canonical flow.
type MapInput struct {
	Packet       packet.Event
	Sequence     uint64
	InterArrival time.Duration
	Channel      uint8
}

// Mapper implements deterministic flow-mode-v1 conversion.
type Mapper struct {
	config MapperConfig
}

// NewMapper validates config and constructs a mapper.
func NewMapper(config MapperConfig) (*Mapper, error) {
	if config.Seed == "" || config.Origin == "" {
		return nil, errors.New("mapper seed and origin are required")
	}
	if config.MinimumNote > 127 || config.MaximumNote > 127 || config.MinimumNote > config.MaximumNote {
		return nil, errors.New("mapper note range must be ordered within 0 through 127")
	}
	if config.MinimumDuration <= 0 || config.MaximumDuration < config.MinimumDuration {
		return nil, errors.New("mapper duration range must be positive and ordered")
	}
	return &Mapper{config: config}, nil
}

// Map deterministically converts packet metadata into a routed note trigger.
func (m *Mapper) Map(input MapInput) (NoteEvent, error) {
	if err := input.Packet.Validate(); err != nil {
		return NoteEvent{}, fmt.Errorf("map packet: %w", err)
	}
	if input.Channel < 1 || input.Channel > 16 {
		return NoteEvent{}, errors.New("map packet: channel must be between 1 and 16")
	}

	key, direction := flow.Canonicalize(input.Packet)
	flowID := key.ID(m.config.Seed)
	identity := sha256.Sum256([]byte(flowID))
	mode := Mode(identity[0] % uint8(modeCount))
	root := identity[1] % 12

	candidates := scaleNotes(mode, root, m.config.MinimumNote, m.config.MaximumNote)
	if len(candidates) == 0 {
		return NoteEvent{}, errors.New("map packet: configured range contains no scale notes")
	}

	index := input.Packet.WireLength + protocolOffset(input.Packet.Protocol) + flagOffset(input.Packet.TCPFlags)
	if direction == flow.DirectionBToA {
		index += (len(candidates) + 1) / 2
	}
	note := candidates[index%len(candidates)]
	velocity := packetVelocity(input.Packet.WireLength, input.Packet.TCPFlags)
	duration := quantizedDuration(input.InterArrival, m.config.MinimumDuration, m.config.MaximumDuration)
	eventID := fmt.Sprintf("%s:%s:%d", m.config.Origin, flowID, input.Sequence)

	event := NoteEvent{
		ID:             eventID,
		Origin:         m.config.Origin,
		Sequence:       input.Sequence,
		MappingVersion: FlowModeV1,
		FlowID:         flowID,
		Mode:           mode,
		Root:           root,
		Note:           note,
		Velocity:       velocity,
		Duration:       duration,
		Channel:        input.Channel,
		CreatedAt:      input.Packet.CapturedAt,
	}
	if err := event.Validate(); err != nil {
		return NoteEvent{}, fmt.Errorf("mapped event: %w", err)
	}
	return event, nil
}

func scaleNotes(mode Mode, root, minimum, maximum uint8) []uint8 {
	var notes []uint8
	for note := int(minimum); note <= int(maximum); note++ {
		pitchClass := (note - int(root)) % 12
		if pitchClass < 0 {
			pitchClass += 12
		}
		for _, interval := range modeIntervals[mode] {
			if pitchClass == int(interval) {
				notes = append(notes, uint8(note))
				break
			}
		}
	}
	return notes
}

func protocolOffset(protocol packet.Protocol) int {
	switch protocol {
	case packet.ProtocolTCP:
		return 0
	case packet.ProtocolUDP:
		return 2
	case packet.ProtocolICMP, packet.ProtocolICMP6:
		return 4
	default:
		return 6
	}
}

func flagOffset(flags packet.TCPFlags) int {
	var offset int
	if flags&packet.TCPFlagSYN != 0 {
		offset += 1
	}
	if flags&packet.TCPFlagFIN != 0 {
		offset += 2
	}
	if flags&packet.TCPFlagRST != 0 {
		offset += 3
	}
	return offset
}

func packetVelocity(wireLength int, flags packet.TCPFlags) uint8 {
	if wireLength < 0 {
		wireLength = 0
	}
	normalized := math.Log1p(float64(wireLength)) / math.Log1p(65535)
	velocity := 1 + int(math.Round(normalized*116))
	if flags&(packet.TCPFlagSYN|packet.TCPFlagFIN|packet.TCPFlagRST) != 0 {
		velocity += 10
	}
	if velocity > 127 {
		velocity = 127
	}
	return uint8(velocity)
}

func quantizedDuration(interArrival, minimum, maximum time.Duration) time.Duration {
	if interArrival <= 0 {
		return minimum
	}
	const quantum = 25 * time.Millisecond
	duration := ((interArrival + quantum/2) / quantum) * quantum
	if duration < minimum {
		return minimum
	}
	if duration > maximum {
		return maximum
	}
	return duration
}
