package capture

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

func TestReplayReadsNormalizedPacketsAndEOF(t *testing.T) {
	data := replayFixture(t)
	source, err := NewReplay(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewReplay() error = %v", err)
	}
	defer source.Close()

	first, err := source.Next(context.Background())
	if err != nil {
		t.Fatalf("Next(first) error = %v", err)
	}
	if string(first.Protocol) != "tcp" || first.Source.Port != 50000 || first.Destination.Port != 443 {
		t.Fatalf("Next(first) = %#v", first)
	}
	second, err := source.Next(context.Background())
	if err != nil {
		t.Fatalf("Next(second) error = %v", err)
	}
	if string(second.Protocol) != "udp" || second.Source.Port != 5353 {
		t.Fatalf("Next(second) = %#v", second)
	}
	if _, err := source.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next(EOF) error = %v, want io.EOF", err)
	}
}

func TestReplayCloseIsIdempotent(t *testing.T) {
	source, err := NewReplay(bytes.NewReader(replayFixture(t)))
	if err != nil {
		t.Fatalf("NewReplay() error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close() second error = %v", err)
	}
	if _, err := source.Next(context.Background()); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("Next() error = %v, want ErrSourceClosed", err)
	}
}

func TestReplayHonorsCanceledContext(t *testing.T) {
	source, err := NewReplay(bytes.NewReader(replayFixture(t)))
	if err != nil {
		t.Fatalf("NewReplay() error = %v", err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() error = %v, want context.Canceled", err)
	}
}

func TestReplayProducesDeterministicNotes(t *testing.T) {
	first := replayNotes(t, replayFixture(t))
	second := replayNotes(t, replayFixture(t))
	want := []replayNote{
		{FlowID: "96071b2befae30b448c72c31", Mode: "ionian", Root: 1, Note: 78, Velocity: 54, Duration: 50 * time.Millisecond, Channel: 1},
		{FlowID: "af775f0bc33ef2db7384a3f1", Mode: "lydian", Root: 5, Note: 89, Velocity: 45, Duration: 125 * time.Millisecond, Channel: 2},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("first replay = %#v, want %#v", first, want)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second replay = %#v, want %#v", second, want)
	}
}

type replayNote struct {
	FlowID   string
	Mode     string
	Root     uint8
	Note     uint8
	Velocity uint8
	Duration time.Duration
	Channel  uint8
}

func replayNotes(t *testing.T, data []byte) []replayNote {
	t.Helper()
	source, err := NewReplay(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewReplay() error = %v", err)
	}
	defer source.Close()
	mapper, err := music.NewMapper(music.MapperConfig{
		Seed:            "replay-seed",
		Origin:          "replay-test",
		MinimumNote:     36,
		MaximumNote:     96,
		MinimumDuration: 50 * time.Millisecond,
		MaximumDuration: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMapper() error = %v", err)
	}

	var result []replayNote
	var previous time.Time
	for sequence := uint64(1); ; sequence++ {
		event, err := source.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		var interArrival time.Duration
		if !previous.IsZero() {
			interArrival = event.CapturedAt.Sub(previous)
		}
		previous = event.CapturedAt
		note, err := mapper.Map(music.MapInput{Packet: event, Sequence: sequence, InterArrival: interArrival, Channel: uint8(sequence)})
		if err != nil {
			t.Fatalf("Map() error = %v", err)
		}
		result = append(result, replayNote{
			FlowID: note.FlowID, Mode: note.Mode.String(), Root: note.Root,
			Note: note.Note, Velocity: note.Velocity, Duration: note.Duration, Channel: note.Channel,
		})
	}
	return result
}

func replayFixture(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := pcapgo.NewWriter(&buffer)
	if err := writer.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatalf("WriteFileHeader() error = %v", err)
	}

	tcpData := serializePacket(t,
		&layers.Ethernet{SrcMAC: []byte{0, 1, 2, 3, 4, 5}, DstMAC: []byte{6, 7, 8, 9, 10, 11}, EthernetType: layers.EthernetTypeIPv4},
		&layers.IPv4{Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP, SrcIP: netip.MustParseAddr("192.0.2.10").AsSlice(), DstIP: netip.MustParseAddr("198.51.100.20").AsSlice()},
		&layers.TCP{SrcPort: 50000, DstPort: 443, SYN: true},
		gopacket.Payload("hello"),
	)
	udpData := serializePacket(t,
		&layers.Ethernet{SrcMAC: []byte{6, 7, 8, 9, 10, 11}, DstMAC: []byte{0, 1, 2, 3, 4, 5}, EthernetType: layers.EthernetTypeIPv6},
		&layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolUDP, SrcIP: netip.MustParseAddr("2001:db8::1").AsSlice(), DstIP: netip.MustParseAddr("2001:db8::2").AsSlice()},
		&layers.UDP{SrcPort: 5353, DstPort: 5353},
		gopacket.Payload("dns"),
	)
	writeFixturePacket(t, writer, time.Unix(100, 0), tcpData)
	writeFixturePacket(t, writer, time.Unix(100, int64(125*time.Millisecond)), udpData)
	return buffer.Bytes()
}

func writeFixturePacket(t *testing.T, writer *pcapgo.Writer, timestamp time.Time, data []byte) {
	t.Helper()
	info := gopacket.CaptureInfo{Timestamp: timestamp, CaptureLength: len(data), Length: len(data)}
	if err := writer.WritePacket(info, data); err != nil {
		t.Fatalf("WritePacket() error = %v", err)
	}
}
