package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/pcapgo"

	packetmeta "github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

type replaySource struct {
	mu       sync.Mutex
	reader   *pcapgo.Reader
	linkType gopacket.Decoder
	closer   io.Closer
	closed   bool
}

// NewReplay constructs a source for a classic PCAP stream. The caller retains
// ownership of reader.
func NewReplay(reader io.Reader) (Source, error) {
	return newReplay(reader, nil)
}

// OpenReplayFile opens a classic PCAP file and transfers file ownership to the
// returned source.
func OpenReplayFile(path string) (Source, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open PCAP file: %w", err)
	}
	source, err := newReplay(file, file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return source, nil
}

func newReplay(reader io.Reader, closer io.Closer) (Source, error) {
	if reader == nil {
		return nil, errors.New("PCAP reader is required")
	}
	pcapReader, err := pcapgo.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("read PCAP header: %w", err)
	}
	return &replaySource{reader: pcapReader, linkType: pcapReader.LinkType(), closer: closer}, nil
}

func (s *replaySource) Next(ctx context.Context) (packetmeta.Event, error) {
	for {
		select {
		case <-ctx.Done():
			return packetmeta.Event{}, ctx.Err()
		default:
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return packetmeta.Event{}, ErrSourceClosed
		}
		data, info, err := s.reader.ReadPacketData()
		s.mu.Unlock()
		if err != nil {
			return packetmeta.Event{}, fmt.Errorf("read PCAP packet: %w", err)
		}
		frame := gopacket.NewPacket(data, s.linkType, gopacket.DecodeOptions{Lazy: true, NoCopy: true})
		event, err := Normalize(frame, info)
		if errors.Is(err, ErrUnsupportedPacket) {
			continue
		}
		return event, err
	}
}

func (s *replaySource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.closer != nil {
		if err := s.closer.Close(); err != nil {
			return fmt.Errorf("close PCAP source: %w", err)
		}
	}
	return nil
}
