# Processing Pipeline

The stage-seven processor composes a packet source, bounded flow registry,
selector, musical mapper, and note sink without coupling those domain packages
to Prometheus.

## Lifecycle and ownership

`pipeline.Processor` owns its packet source and may run once. It closes that
source after EOF, cancellation, or a terminal capture error, then waits for its
packet and note workers to stop. A note sink must honor context cancellation so
shutdown cannot be held indefinitely by an adapter.

For an ordinary finite source such as PCAP replay, accepted packets and mapped
notes drain in order before `Run` returns. During cancellation, queued work is
discarded and counted rather than played after shutdown begins.

## Bounded overload behavior

The packet and note queues have fixed, required capacities. Producers never
block when a queue is full:

- A full packet queue drops the newly captured packet.
- A full note queue drops the newly mapped note.
- Cancellation drops pending packet and note work.
- A sink write failure drops that note and allows the consumer to continue.

These policies keep capture independent of slow mapping, MIDI, peer, and UI
consumers. MIDI rate, retrigger, and polyphony controls belong to the scheduler
in delivery stage eight.

## Prometheus contract

`metrics.New("musical_packets")` returns an isolated registry containing the
standard Go and process collectors plus a concurrency-safe pipeline observer.
The base HTTP handler exposes that registry at `GET /metrics`, with injectable
checks at `GET /healthz` and `GET /readyz`.

The initial pipeline metrics are:

```text
musical_packets_packets_captured_total{protocol}
musical_packets_packet_bytes_captured_total{protocol}
musical_packets_packet_capture_errors_total{reason}
musical_packets_packet_events_dropped_total{stage,reason}
musical_packets_packet_queue_depth
musical_packets_packet_queue_capacity
musical_packets_note_queue_depth
musical_packets_note_queue_capacity
musical_packets_flows_active
musical_packets_flow_registry_evictions_total{reason}
musical_packets_flow_selections_total{state,tier}
musical_packets_mapping_events_total{mode,result}
musical_packets_mapping_duration_seconds
musical_packets_mapping_note_velocity
musical_packets_mapping_note_duration_seconds
musical_packets_packet_processing_duration_seconds
```

Every label value originates from a bounded enum controlled by the application.
Flow IDs, IP addresses, ports, rule IDs, device names, and peer identities are
deliberately excluded to prevent cardinality growth.
