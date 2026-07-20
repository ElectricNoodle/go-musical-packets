# Musical Packets

Musical Packets turns selected network traffic into deterministic MIDI notes.
It is being built for macOS and Debian Linux with standalone, edge, and host
runtime roles.

The accepted architecture and delivery plan is in [docs/PLAN.md](docs/PLAN.md).

## Development status

The project is in its foundation stage. Domain contracts and validation are
being established before native packet-capture and MIDI integrations are added.

## Commands

```sh
make test
make race
make vet
make build
./bin/musical-packets version
```

