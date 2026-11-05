# Stats and drop counters

Both binaries log traffic counters at every `stats_interval` (default `10s`). This page explains the log format, each field and where each drop counter is incremented. The counters are defined in `internal/stats/stats.go`.

## Log format

Logs go to standard error in the zap console format: a timestamp in the local time zone with its UTC offset, the level, the message, and the fields as a JSON object, separated by tabs. Each report has one `channel stats` line per channel, ordered by upstream name then ascending channel ID, followed by one `global drops` line per upstream (one line with no `upstream` field on the server).

This report comes from the server in the [quick start](../README.md#quick-start), after three datagrams of 7 bytes each:

```text
2026-11-04T15:45:18.973-0500	INFO	channel stats	{"side": "server", "transport": "tcp", "channel": 1, "name": "demo", "pps": 0.6, "mbps": 0, "packets": 3, "bytes": 21, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
2026-11-04T15:45:18.974-0500	INFO	global drops	{"side": "server", "transport": "tcp", "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
```

The first report appears one interval after startup. No report is written at shutdown.

## Fields

| Field | Line | Meaning |
| --- | --- | --- |
| `side` | both | `server` or `client` |
| `transport` | both | `tcp`, `quic` or `kcp`: the transport of the server, or of that client upstream. Present on every `channel stats` and `global drops` line. |
| `upstream` | client | Upstream `name` from the configuration. Present on every client `channel stats` and `global drops` line. Absent on the server. |
| `channel` | `channel stats` | Channel ID |
| `name` | `channel stats` | Channel `name` from the configuration, or empty |
| `pps` | `channel stats` | Packets per second since the previous report, rounded to two decimals |
| `mbps` | `channel stats` | Payload megabits (10^6 bits) per second since the previous report, rounded to two decimals |
| `packets` | `channel stats` | Datagrams forwarded since startup |
| `bytes` | `channel stats` | Payload bytes forwarded since startup. UDP, IP and frame headers are not included. |
| `drop_<reason>` | both | Datagrams dropped for that reason since startup |

On the server, `packets` and `bytes` count datagrams handed to the session (TCP write, KCP write or QUIC datagram or stream write). With `quic`, a datagram accepted by the QUIC stack can still be dropped before it is sent, and nothing counts that. On the client, they count datagrams handed to the local UDP socket. A successful write on the client does not prove that the target received the datagram.

The drop counters are cumulative. Both lines always carry all seven `drop_` fields, including those that a side never increments.

## Drop reasons

| Reason | Side | Line | Cause |
| --- | --- | --- | --- |
| `queue_full` | server | `channel stats` | The queue of `queue_size` datagrams was full. The newest datagram was dropped. |
| `unknown_channel` | server | `global drops` | A captured datagram matched no channel |
| `unknown_channel` | client | `global drops` | A frame carried a channel ID that that upstream's channel list does not include |
| `oversized` | server | `channel stats` | The payload was larger than `max_payload` |
| `fragment` | server | `global drops` | An IPv4 first fragment was captured in `afpacket` mode. See [Fragments](capture-modes.md#fragments). |
| `capture_drop` | server | `global drops` | AF_PACKET kernel drops (`PACKET_STATISTICS`). |
| `capture_freeze` | server | `global drops` | AF_PACKET TPACKET_V3 ring freezes (`tp_freeze_q_cnt`). Userspace was too slow releasing blocks. |
| `no_client` | server | `channel stats` | A datagram arrived while no client was connected |
| `write_error` | server | `channel stats` | Writing the frame to the session failed (TCP, KCP, or a QUIC datagram or stream write). The server then closes the connection. |
| `write_error` | client | `channel stats` | Sending the datagram to `target` failed. On macOS this includes payloads larger than `net.inet.udp.maxdgram` (9216 bytes by default). |

The server reads `capture_drop`, `capture_freeze` and `fragment` from the capture layer just before each report, so these counters change only at report time.

Some losses are not counted:

- Datagrams still in the server queue when a client connects are discarded without a count.
- With `quic`, datagrams that the network loses or that the QUIC stack drops (full send or receive queue) are not counted on either side. Payloads above the datagram limit are not dropped: they are sent on the stream.
- With `kcp`, the session retransmits, so loss shows up as latency and, when the send window is full, as `queue_full`, not as a counter of its own.
- Frames that the `afpacket` parser cannot read are skipped without a count.
- IPv4 fragments after the first, and all IPv6 fragments, are rejected by the kernel filter in `afpacket` mode.
- In `socket` mode, datagrams that the kernel drops because a socket receive buffer is full never reach the server.

## Compression stats

When `compression` is `zstd` or `s2`, each report has one more line per server or per client upstream, after the `global drops` lines. It is absent when compression is `none`.

```text
2026-11-05T10:12:20.114-0500	INFO	compression stats	{"side": "client", "upstream": "east", "transport": "quic", "algorithm": "zstd", "tx_bytes_in": 0, "tx_bytes_out": 0, "tx_ratio": 0, "tx_raw_frames": 0, "rx_bytes_in": 412873, "rx_bytes_out": 1316640, "rx_ratio": 3.19, "rx_decompress_errors": 0, "rx_oversized": 0}
```

| Field | Meaning |
| --- | --- |
| `side`, `upstream`, `transport` | As in the other lines. `upstream` is absent on the server. |
| `algorithm` | `zstd` or `s2` |
| `tx_bytes_in` | UDP payload bytes this side sent, before compression |
| `tx_bytes_out` | Bytes of those payloads on the wire, after compression. Raw payloads count the same in both fields. Frame headers are not included. |
| `tx_ratio` | `tx_bytes_in` divided by `tx_bytes_out`, rounded to two decimals. `3.19` means the wire carried about a third of the original bytes. `0` before any traffic. |
| `tx_raw_frames` | Payloads sent uncompressed because they were shorter than 16 bytes or compression did not make them smaller |
| `rx_bytes_in` | Payload bytes received, as they arrived on the wire |
| `rx_bytes_out` | The same payloads after decompression |
| `rx_ratio` | `rx_bytes_out` divided by `rx_bytes_in` |
| `rx_decompress_errors` | Frames dropped because they could not be decoded |
| `rx_oversized` | Frames dropped because they would expand beyond 65535 bytes (a decompression bomb) |

All values are cumulative since startup. The server's data flows to the client, so on a healthy tunnel the server shows `tx_*` and the client `rx_*`; the other direction stays at 0. The server `packets` and `bytes` fields of `channel stats` still count original payload bytes. Frames dropped for `rx_decompress_errors` and `rx_oversized` are not counted in the per-channel drop counters.

A low `tx_ratio` close to 1 with a high `tx_raw_frames` means the data does not compress; set `compression: none` to save CPU. A non-zero `rx_decompress_errors` on a session that worked before points at corruption, for example a KCP session without `kcp.key` crossing a device that rewrites packets.

## Reading the counters

When no client is connected, the server counts every matching datagram as `no_client`. This server report was taken while a client failed to authenticate; one datagram arrived on channel 1 and one datagram on port 5001 came from a source port that no channel accepts:

```text
2026-11-04T15:45:40.891-0500	INFO	channel stats	{"side": "server", "transport": "tcp", "channel": 1, "name": "demo", "pps": 0, "mbps": 0, "packets": 0, "bytes": 0, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 1, "drop_write_error": 0}
2026-11-04T15:45:40.891-0500	INFO	channel stats	{"side": "server", "transport": "tcp", "channel": 2, "name": "pinned", "pps": 0, "mbps": 0, "packets": 0, "bytes": 0, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
2026-11-04T15:45:40.891-0500	INFO	global drops	{"side": "server", "transport": "tcp", "drop_queue_full": 0, "drop_unknown_channel": 1, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
```

On Linux, when nothing listens on a client `target`, the kernel reports the ICMP error on a later send, so only some datagrams are counted as `write_error` and the rest still appear in `packets`. This client report was taken after three datagrams were sent with no receiver on the target port:

```text
2026-11-04T15:47:05.959-0500	INFO	channel stats	{"side": "client", "transport": "tcp", "channel": 1, "name": "demo", "pps": 0.4, "mbps": 0, "packets": 2, "bytes": 4, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 1}
```

A steady rise in `queue_full` means that the TCP connection cannot carry the input rate. A rise in `capture_drop` or `capture_freeze` means that the server does not read the AF_PACKET ring fast enough. A freeze lasting many minutes with flat `capture_freeze` usually points at the TCP tunnel (see `TCP_USER_TIMEOUT` in [protocol.md](protocol.md)), not the ring.
