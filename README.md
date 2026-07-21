# Musical Packets

Musical Packets turns selected network traffic into deterministic MIDI notes.
It is being built for macOS and Debian Linux with standalone, edge, and host
runtime roles.

The accepted architecture and delivery plan is in [docs/PLAN.md](docs/PLAN.md).

## Development status

Delivery stages 1 through 10 are implemented. The local management API includes
transactional configuration, capture-interface and MIDI discovery, MIDI
audition and panic, bounded flow snapshots, temporary mute/solo controls, and
ordered persistent-rule mutations. Stage 11 is underway: the embedded
React/TypeScript foundation and first setup-assistant slice are implemented;
the first bounded flow explorer now supports live search, sorting, selection,
and temporary mute/solo control. Rule and musical annotations plus persistent
pinning are next.

## Commands

```sh
make test
make race
make vet
make build
./bin/musical-packets version
./bin/musical-packets interfaces
./bin/musical-packets devices
cp config.example.yaml config.local.yaml
chmod 600 config.local.yaml
./bin/musical-packets validate-config --config config.local.yaml
./bin/musical-packets run --config config.local.yaml
./bin/musical-packets replay recording.pcap --config config.replay.example.yaml
```

The owner-only mode on the local config is required for transactional runtime
writes; `*.local.yaml` is ignored by Git.

Native capture and MIDI prerequisites are documented in
[docs/platform-spike.md](docs/platform-spike.md).
Pipeline ownership, overload behavior, and the initial Prometheus contract are
documented in [docs/pipeline.md](docs/pipeline.md).
MIDI scheduling and reconnect behavior are documented in
[docs/midi.md](docs/midi.md).
PCAP replay configuration, pacing, and completion behavior are documented in
[docs/replay.md](docs/replay.md).
Standalone configuration, probes, exclusions, and shutdown are documented in
[docs/standalone.md](docs/standalone.md).
The local transactional HTTP contract is documented in
[docs/management-api.md](docs/management-api.md).
Frontend development, embedding, and setup behavior are documented in
[docs/frontend.md](docs/frontend.md).
