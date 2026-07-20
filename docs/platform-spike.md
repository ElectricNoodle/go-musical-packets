# Platform capture and MIDI spike

This milestone proves the native boundaries selected in the architecture plan.
The application uses GoPacket/libpcap for packet capture and GoMIDI/RtMidi for
MIDI output. Pure interfaces and normalization tests keep most development and
testing independent of native devices.

RtMidi is executed in a private helper process. Some native initialization
failures are implemented as uncaught C++ exceptions upstream; process isolation
turns those failures into retryable errors instead of terminating the capture,
networking, metrics, and UI process.

## macOS

Install the Xcode Command Line Tools. Their macOS SDK supplies libpcap and the
CoreMIDI, CoreAudio, CoreServices, and CoreFoundation frameworks required by the
CGO adapters.

List capture and MIDI devices:

```sh
go run ./cmd/musical-packets interfaces
go run ./cmd/musical-packets devices
```

Live capture may require access to `/dev/bpf*`. The eventual installer will use
a least-privilege capture setup; do not run an unattended application as root.
The IAC Driver in Audio MIDI Setup can provide a virtual output for manual tests.

An opt-in test probes live-open permission without retaining packets:

```sh
MUSICAL_PACKETS_LIVE_INTERFACE=en0 go test ./internal/capture -run LiveOpenIntegration
```

## Debian

Install the compiler, libpcap headers, and ALSA development headers:

```sh
sudo apt-get update
sudo apt-get install build-essential libpcap-dev libasound2-dev
```

The same `interfaces` and `devices` commands enumerate libpcap and ALSA/RtMidi
outputs. Packet capture requires root, an appropriate capability such as
`CAP_NET_RAW`, or another deliberately configured capture permission.

## Build variants

Native adapters are compiled only when CGO is enabled on macOS or Linux. Other
builds retain the domain interfaces and return an explicit unavailable error.
This keeps unit tests and future non-native tooling portable.
