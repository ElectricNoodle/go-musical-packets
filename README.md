# Musical Packets

Musical Packets turns selected network traffic into deterministic MIDI notes.
It is being built for macOS and Debian Linux with standalone, edge, and host
runtime roles.

The accepted architecture and delivery plan is in [docs/PLAN.md](docs/PLAN.md).

## Development status

Delivery stages 1 through 9 are implemented: configuration and lifecycle
foundations, native capture/MIDI feasibility adapters, deterministic musical
mapping, bounded flow selection, PCAP replay, and the instrumented live
processing pipeline, safe MIDI scheduling and hot-plug recovery, plus the
strictly configured standalone runtime. The transactional management API is the
next stage.

## Commands

```sh
make test
make race
make vet
make build
./bin/musical-packets version
./bin/musical-packets interfaces
./bin/musical-packets devices
./bin/musical-packets validate-config --config config.example.yaml
./bin/musical-packets run --config config.example.yaml
```

Native capture and MIDI prerequisites are documented in
[docs/platform-spike.md](docs/platform-spike.md).
Pipeline ownership, overload behavior, and the initial Prometheus contract are
documented in [docs/pipeline.md](docs/pipeline.md).
MIDI scheduling and reconnect behavior are documented in
[docs/midi.md](docs/midi.md).
Standalone configuration, probes, exclusions, and shutdown are documented in
[docs/standalone.md](docs/standalone.md).
