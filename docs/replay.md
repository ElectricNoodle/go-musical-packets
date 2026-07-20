# Deterministic PCAP replay

Offline replay uses the same normalization and musical mapping code as live
capture. It accepts classic PCAP streams, preserves capture timestamps, skips
unsupported non-IP frames, and returns `io.EOF` after the final packet.

`capture.NewReplay` leaves ownership of an `io.Reader` with its caller.
`capture.OpenReplayFile` owns and closes the opened file. Both implement the
same `capture.Source` interface as the libpcap backend.

The replay test fixture contains IPv4/TCP and IPv6/UDP frames. Its golden test
fixes the flow IDs, modes, roots, notes, velocity, duration, and channels emitted
by `flow-mode-v1`. Any future mapping change must therefore be intentional and
versioned rather than silently changing existing performances.

The user-facing `replay` command will be composed after strict YAML loading and
selector-rule persistence are implemented; this milestone establishes its
deterministic source and regression boundary.

