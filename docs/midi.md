# MIDI Scheduling and Recovery

The MIDI subsystem separates timing and safety from native device ownership.
The scheduler accepts validated `music.NoteEvent` values, while the manager
discovers and maintains a selected output through the process-isolated native
driver.

## Scheduler guarantees

The scheduler sends Note On immediately and creates one owned timer for its
Note Off. Generation tokens prevent an expired timer from stopping a newer
retrigger of the same channel and pitch.

Global safety controls provide:

- A sliding one-second maximum note rate.
- A maximum number of simultaneously active channel/pitch pairs.
- A minimum same-note retrigger interval.
- Note Off before an accepted same-note retrigger.
- Channel-wide All Notes Off fallback after a failed Note Off.
- All Notes Off on all 16 channels during panic and close.

Closing the scheduler stops every pending timer and permanently rejects new
notes. Panic performs the same reset but leaves the scheduler reusable.

## Device recovery

The manager performs discovery immediately and at the configured poll interval.
Selection precedence remains exact configured name, configured regular
expression, then the first available output. Starting without a MIDI device is
normal: sends report `ErrOutputUnavailable`, and polling continues.

`Manager.Ready` closes after the first discovery attempt, whether or not that
attempt finds a device, or if the manager terminates before discovery begins.
Runtime composition waits for this boundary before starting capture so a
quickly terminating packet source cannot overtake initial MIDI discovery.

A failed send closes and invalidates the current output. A later poll reopens
the preferred device without restarting capture or mapping. Cancellation closes
the output before the driver. Runtime composition must close or panic the
scheduler before canceling the manager so reset messages have a live port when
possible.

Scheduler panic and close still attempt all 16 reset messages while disconnected,
but an unavailable output is not itself a shutdown failure. Other reset and
driver-close errors remain visible to the caller.

## Metrics

The Prometheus bundle includes these bounded-cardinality MIDI metrics:

```text
musical_packets_midi_device_connected
musical_packets_midi_reconnects_total{result}
musical_packets_midi_errors_total{operation}
musical_packets_midi_notes_total{channel,result}
musical_packets_midi_writes_total{operation,result}
musical_packets_midi_write_duration_seconds{operation}
musical_packets_midi_active_notes{channel}
musical_packets_midi_active_notes_current
```

Channels are bounded to the user-facing values 1 through 16. Device names and
note origins are intentionally not labels.
