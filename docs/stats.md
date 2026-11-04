# Stats and drop counters

Both binaries log traffic counters at every `stats_interval` (default `10s`). This page explains the log format, each field and where each drop counter is incremented. The counters are defined in `internal/stats/stats.go`.

## Log format

Logs go to standard error in the zap console format: a timestamp in the local time zone with its UTC offset, the level, the message, and the fields as a JSON object, separated by tabs. Each report has one `channel stats` line per channel, in ascending channel ID order, followed by one `global drops` line.

This report comes from the server in the [quick start](../README.md#quick-start), after three datagrams of 7 bytes each:

```text
2026-11-04T15:45:18.973-0500	INFO	channel stats	{"side": "server", "channel": 1, "name": "demo", "pps": 0.6, "mbps": 0, "packets": 3, "bytes": 21, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
2026-11-04T15:45:18.974-0500	INFO	global drops	{"side": "server", "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
```

The first report appears one interval after startup. No report is written at shutdown.

## Fields

| Field | Line | Meaning |
| --- | --- | --- |
| `side` | both | `server` or `client` |
| `channel` | `channel stats` | Channel ID |
| `name` | `channel stats` | Channel `name` from the configuration, or empty |
| `pps` | `channel stats` | Packets per second since the previous report, rounded to two decimals |
| `mbps` | `channel stats` | Payload megabits (10^6 bits) per second since the previous report, rounded to two decimals |
| `packets` | `channel stats` | Datagrams forwarded since startup |
| `bytes` | `channel stats` | Payload bytes forwarded since startup. UDP, IP and frame headers are not included. |
| `drop_<reason>` | both | Datagrams dropped for that reason since startup |

On the server, `packets` and `bytes` count datagrams written to the TCP connection. On the client, they count datagrams handed to the local UDP socket. A successful write on the client does not prove that the target received the datagram.

The drop counters are cumulative. Both lines always carry all seven `drop_` fields, including those that a side never increments.

## Drop reasons

| Reason | Side | Line | Cause |
| --- | --- | --- | --- |
| `queue_full` | server | `channel stats` | The queue of `queue_size` datagrams was full. The newest datagram was dropped. |
| `unknown_channel` | server | `global drops` | A captured datagram matched no channel |
| `unknown_channel` | client | `global drops` | A frame carried a channel ID that the client configuration does not list |
| `oversized` | server | `channel stats` | The payload was larger than `max_payload` |
| `fragment` | server | `global drops` | An IPv4 first fragment was captured in `afpacket` mode. See [Fragments](capture-modes.md#fragments). |
| `capture_drop` | server | `global drops` | The kernel dropped frames because the AF_PACKET ring was full. Always 0 in `socket` mode. |
| `no_client` | server | `channel stats` | A datagram arrived while no client was connected |
| `write_error` | server | `channel stats` | Writing the frame to the TCP connection failed. The server then closes the connection. |
| `write_error` | client | `channel stats` | Sending the datagram to `target` failed |

The server reads `capture_drop` and `fragment` from the capture layer just before each report, so these two counters change only at report time.

Some losses are not counted:

- Datagrams still in the server queue when a client connects are discarded without a count.
- Frames that the `afpacket` parser cannot read are skipped without a count.
- IPv4 fragments after the first, and all IPv6 fragments, are rejected by the kernel filter in `afpacket` mode.
- In `socket` mode, datagrams that the kernel drops because a socket receive buffer is full never reach the server.

## Reading the counters

When no client is connected, the server counts every matching datagram as `no_client`. This server report was taken while a client failed to authenticate; one datagram arrived on channel 1 and one datagram on port 5001 came from a source port that no channel accepts:

```text
2026-11-04T15:45:40.891-0500	INFO	channel stats	{"side": "server", "channel": 1, "name": "demo", "pps": 0, "mbps": 0, "packets": 0, "bytes": 0, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 1, "drop_write_error": 0}
2026-11-04T15:45:40.891-0500	INFO	channel stats	{"side": "server", "channel": 2, "name": "pinned", "pps": 0, "mbps": 0, "packets": 0, "bytes": 0, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
2026-11-04T15:45:40.891-0500	INFO	global drops	{"side": "server", "drop_queue_full": 0, "drop_unknown_channel": 1, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
```

On Linux, when nothing listens on a client `target`, the kernel reports the ICMP error on a later send, so only some datagrams are counted as `write_error` and the rest still appear in `packets`. This client report was taken after three datagrams were sent with no receiver on the target port:

```text
2026-11-04T15:47:05.959-0500	INFO	channel stats	{"side": "client", "channel": 1, "name": "demo", "pps": 0.4, "mbps": 0, "packets": 2, "bytes": 4, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 1}
```

A steady rise in `queue_full` means that the TCP connection cannot carry the input rate. A rise in `capture_drop` means that the server does not read the AF_PACKET ring fast enough.
