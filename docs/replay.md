# Deterministic PCAP replay

Offline replay uses the same normalization, flow selection, musical mapping,
and MIDI scheduling code as live capture. It accepts classic PCAP files,
preserves capture timestamps, skips unsupported non-IP frames, and treats the
end of a valid recording as successful completion.

```sh
musical-packets replay recording.pcap --config config.yaml
```

The recording and `--config` may appear in either order. Configuration remains
strictly decoded and validated. Replay currently requires the `standalone`
role, `capture.enabled: true`, peer transport disabled, and an empty
`capture.bpf`. Offline BPF evaluation is not implemented, so a configured BPF
is rejected instead of being silently ignored; selector rules still choose
which normalized packets are ignored, monitored, or played. The live capture
interface, snapshot, promiscuous, and HTTP server settings are not used.

Packets are paced against their capture timestamps. The first supported packet
is immediate, overdue or non-monotonic timestamps do not wait, and waits can be
cancelled with SIGINT or SIGTERM. Capture-time flow expiry and inter-arrival
mapping remain deterministic. Physical MIDI acceptance is deliberately not
claimed to be deterministic: bounded queues can drop bursts, and rate,
polyphony, and retrigger protection operate against wall time.

When MIDI is enabled, a selected output must be available before the recording
is consumed. Setting `midi.enabled: false` is a valid processing-only replay and
does not initialize a MIDI driver; notes selected for play are rejected by the
disabled sink. Replay does not start the HTTP management listener or live
packet capture.

`capture.NewReplay` leaves ownership of an `io.Reader` with its caller.
`capture.OpenReplayFile` owns and closes the opened file. Both implement the
same `capture.Source` interface as the libpcap backend.

The replay test fixture contains IPv4/TCP and IPv6/UDP frames. Its golden test
fixes the flow IDs, modes, roots, notes, velocity, duration, and channels emitted
by `flow-mode-v1`. Any future mapping change must therefore be intentional and
versioned rather than silently changing existing performances.

At EOF, accepted pipeline work is drained and the MIDI scheduler immediately
sends its final reset while the output remains connected; replay does not wait
for every mapped note duration to elapse. The source, output, and driver are
then closed in ownership order. Malformed files, read failures, unavailable
enabled MIDI output, and reported source, MIDI-reset, or driver cleanup failures
produce a non-zero exit status.
