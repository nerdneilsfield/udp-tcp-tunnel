# Configuration reference

This page describes every key in the server and client configuration files, with its type, default and validation rules. The values come from `internal/config/config.go`.

## Loading rules

Both binaries read one YAML file, given with `-c` or `--config`. The server reads `server.yaml` and the client reads `client.yaml` from the current directory when the flag is absent.

The loader applies these rules in order:

1. The file is decoded with unknown keys rejected. A misspelled key stops the program with a parse error.
2. Defaults replace keys that are absent or set to zero. An empty file is accepted at this step.
3. Validation checks every key and reports all problems in one error, one problem per line.

Durations use Go duration syntax, such as `500ms`, `10s` or `1m30s`. A bare number is rejected:

```text
error: parse config d.yaml: yaml: unmarshal errors:
  line 1: cannot unmarshal !!int `10` into time.Duration
```

Ports and channel IDs are unsigned 16-bit integers. A value above 65535 is a parse error:

```text
error: parse config p.yaml: yaml: unmarshal errors:
  line 3: cannot unmarshal !!int `70000` into uint16
```

Validation errors start with `invalid server config <path>:` or `invalid client config <path>:`. A file that cannot be read produces `read config: <reason>`. In each case the binary exits with status 1.

Addresses written as `host:port` must have a numeric port between 1 and 65535. A host name is allowed wherever a host is allowed. The address checks produce these messages, after the key name:

| Message | Cause |
| --- | --- |
| `required` | The key is empty |
| `"<value>" is not host:port` | The value cannot be split into host and port |
| `"<value>" has no host` | The host is empty where one is required |
| `"<value>" has an invalid port` | The port is not a number between 1 and 65535 |

## Server keys

### listen

Address on which the server accepts the client: a TCP port for `transport: tcp`, a UDP port for `quic` and `kcp`.

- Type: `host:port` string. The host can be empty to listen on all addresses.
- Default: `:9000`
- Error: `listen: <address message>`

With `quic` or `kcp`, open the port for UDP in the firewall, not TCP.

The server logs a warning at startup when this address is not loopback and either TLS or the token is missing. See [Protect the tunnel](../README.md#protect-the-tunnel).

### transport

How the server talks to the client. The client must use the same transport for this server. See [protocol.md](protocol.md) for what each transport puts on the wire.

- Type: string, `tcp`, `quic` or `kcp`
- Default: `tcp`
- Error: `transport: "<value>" must be "tcp", "quic" or "kcp"`

| Value | Transport | `tls` | `kcp` |
| --- | --- | --- | --- |
| `tcp` | TCP | Optional | Ignored |
| `quic` | QUIC over UDP, TLS 1.3 | Required | Ignored |
| `kcp` | KCP over UDP | Must be off | Used |

Errors:

- `tls: must be enabled for transport "quic"`
- `tls: not supported with transport "kcp"; set kcp.key to encrypt`

### compression

Payload compression for the datagrams that the server sends. The client must use the same algorithm; otherwise the handshake fails (see [protocol.md](protocol.md#compression)).

Two forms are accepted:

```yaml
compression: zstd                  # shorthand: algorithm with the default level
compression:
  algorithm: zstd
  level: 1
```

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `algorithm` | string | `none` | `none`, `zstd` or `s2` |
| `level` | integer | `1` | Compression level. `zstd`: 1 fastest, 2 default, 3 better, 4 best. `s2`: 1 standard, 2 better, 3 best. Not allowed with `none`. |

- Default: `none`. The wire format is then identical to v0.2.0, so a v0.2.0 server or client interoperates with it.
- `zstd` is [klauspost/compress/zstd](https://github.com/klauspost/compress/tree/master/zstd). It gives the best ratio. Level 1 costs little CPU and is the right default.
- `s2` is [klauspost/compress/s2](https://github.com/klauspost/compress/tree/master/s2), a faster, Snappy-compatible codec with a lower ratio. Choose it when CPU or latency matters more than bandwidth.
- Each datagram is compressed on its own, so very small or random payloads gain nothing. A payload shorter than 16 bytes, or one that does not get smaller, is sent as it is and counted as `tx_raw_frames` in the [stats](stats.md#compression-stats).
- `level` only affects the sender. The receiver decodes any level of the same algorithm.
- It works with every `transport`. With `quic`, compression also lets larger payloads fit into a datagram.

Errors:

- `compression: algorithm "<value>" must be "none", "zstd" or "s2"`
- `compression: level for zstd must be between 1 and 4`
- `compression: level for s2 must be between 1 and 3`
- `compression: level is not used with algorithm "none"`
- `field <name> not found in compression`, for an unknown key in the mapping

### capture

How the server obtains UDP datagrams. [capture-modes.md](capture-modes.md) describes both modes.

- Type: string, `socket` or `afpacket`
- Default: `socket`
- Error: `capture: "<value>" must be "socket" or "afpacket"`

### udp_bind

Local IP address on which `socket` mode binds the UDP ports.

- Type: IP address string, IPv4 or IPv6
- Default: `0.0.0.0`
- Used only when `capture` is `socket`. It is not validated in `afpacket` mode.
- Error: `udp_bind: "<value>" is not an IP address`

### interface

Network interface that `afpacket` mode captures on, for example `eth0`.

- Type: string
- Default: none
- Required when `capture` is `afpacket`. Ignored in `socket` mode.
- Error: `interface: required when capture is "afpacket"`

### queue_size

Number of datagrams that can wait between capture and the session writer. When the queue is full, the newest datagram is dropped and counted as `queue_full`.

- Type: integer
- Default: `1024`. A value of `0` selects the default.
- Range: 1 or more
- Error: `queue_size: must be at least 1`

### max_payload

Largest UDP payload, in bytes, that the server forwards. Larger datagrams are dropped and counted as `oversized`.

- Type: integer
- Default: `65535`. A value of `0` selects the default.
- Range: 1 to 65535
- Error: `max_payload: must be between 1 and 65535`

### stats_interval

Interval between stats log lines. See [stats.md](stats.md).

- Type: duration
- Default: `10s`. A value of `0s` selects the default.
- Range: positive
- Error: `stats_interval: must be positive`

### token

Shared secret that the client must present during the handshake. The server compares it in constant time. An empty token on both sides is accepted.

- Type: string
- Default: empty
- Range: at most 255 bytes
- Error: `token: longer than 255 bytes`

Without TLS the token crosses the network in clear text. With `transport: kcp` the token is in clear text unless `kcp.key` is set.

### tls

TLS settings for the tunnel. With `tcp` the server accepts TLS 1.2 and later. With `quic` TLS is mandatory and the version is 1.3. With `kcp` TLS is not available.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `tls.enabled` | boolean | `false` | Serve the tunnel over TLS |
| `tls.cert_file` | path | none | PEM certificate, required when TLS is enabled |
| `tls.key_file` | path | none | PEM private key, required when TLS is enabled |

Errors:

- `tls: cert_file and key_file are required when tls.enabled is true`
- `tls: cert_file/key_file are set but tls.enabled is false`

The certificate and key are loaded when the server starts. A file that cannot be loaded stops the server with `load tls certificate: <reason>`.

### kcp

Settings of the KCP transport. They are validated only when `transport` is `kcp`. Every field is optional.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `kcp.key` | string | empty | Passphrase. A non-empty value encrypts the session with AES-256; the key is derived with PBKDF2-HMAC-SHA256. Must be identical on both ends. Never logged. |
| `kcp.nodelay` | boolean | `true` | KCP no-delay mode: lower retransmission timeout |
| `kcp.interval` | duration | `20ms` | Internal update interval, 10ms to 1s |
| `kcp.resend` | integer | `2` | Fast resend after this many skipped ACKs, 0 to 10 (0 disables) |
| `kcp.no_congestion` | boolean | `true` | Turn off KCP congestion control |
| `kcp.send_window` | integer | `1024` | Send window in packets, 1 to 32768 |
| `kcp.recv_window` | integer | `1024` | Receive window in packets, 1 to 32768 |
| `kcp.mtu` | integer | `1350` | Packet size in bytes, 200 to 1500, including KCP overhead |
| `kcp.data_shards` | integer | `0` | Forward error correction: data shards per group. Must match the other end. |
| `kcp.parity_shards` | integer | `0` | FEC parity shards per group. Set both shard counts or neither; their sum is at most 255. Must match the other end. |

The defaults are the fast mode of kcp-go (`nodelay=1, interval=20, resend=2, nc=1`). `key`, `data_shards` and `parity_shards` change the packet format and must be equal on both ends; the other values tune the local end only. `no_congestion: true` sends at full speed regardless of loss, which suits a dedicated link and can hurt a shared one. FEC with 10 data and 3 parity shards adds 30% bandwidth.

Without `key`, KCP is not encrypted: the token and all datagrams cross the network in clear text. With a key the session is encrypted, but KCP does not authenticate packets, so the key does not replace the `token` and is weaker than `quic`.

A session is dropped after 30 seconds without a frame from the peer. Both ends send a heartbeat every 10 seconds when idle. These two values are not configurable.

Errors, prefixed with `kcp: `: `interval: must be between 10ms and 1s`, `resend: must be between 0 and 10`, `send_window: must be between 1 and 32768`, `recv_window: ...`, `mtu: must be between 200 and 1500`, `data_shards and parity_shards: set both to positive values, or neither`, `data_shards + parity_shards: at most 255`.

### channels

List of channels. At least one is required.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `id` | integer, 1 to 65535 | none | Channel ID, shared with the client configuration. Must be unique. |
| `name` | string | empty | Label used in stats log lines |
| `source_ip` | IP address string | any | Match only datagrams from this source address |
| `source_port` | integer, 0 to 65535 | `0` (any) | Match only datagrams from this source port |
| `destination_port` | integer, 1 to 65535 | none | Match datagrams sent to this port. Required. |

When several channels match one datagram, the most specific one wins: a channel with both `source_ip` and `source_port` first, then `source_ip` only, then `source_port` only, then `destination_port` only. IPv4-mapped IPv6 source addresses are compared as IPv4 addresses. A datagram that matches no channel is dropped and counted as `unknown_channel`.

In `socket` mode, the server binds each distinct `destination_port` once, so several channels can share a port.

Errors, where `<i>` is the position of the channel in the list, starting at 0:

- `channels: at least one channel is required`
- `channels[<i>]: id must be non-zero`
- `channels[<i>]: id <id> already used by channels[<j>]`
- `channels[<i>]: destination_port is required`
- `channels[<i>]: source_ip "<value>" is not an IP address`
- `channels[<i>]: same match rule as channels[<j>]`

## Client keys

The client accepts two mutually exclusive shapes:

1. **Single upstream.** Set top-level `server` and `channels`. `Normalize` turns this into one upstream named `default`.
2. **Multiple upstreams.** Set `upstreams` to a non-empty list. Do not set top-level `server` or `channels`.

Top-level `transport`, `compression`, `token`, `tls`, `kcp`, `reconnect_backoff_min` and `reconnect_backoff_max` are shared defaults. In the multi-upstream shape, an upstream that omits one of them inherits the top-level value. `stats_interval` is always top-level.

Mutual-exclusion errors:

- `server: cannot be set together with upstreams`
- `channels: cannot be set together with upstreams`

### server

Address of the server. Used only in the single-upstream shape.

- Type: `host:port` string. The host is required.
- Default: none
- Error after normalize: `upstreams[0]: server: <address message>`

### transport

Transport used to reach the server: `tcp` (default), `quic` or `kcp`, as described under the [server key](#transport). In the single-upstream shape it applies to the one upstream. In the multi-upstream shape it is the default for upstreams that omit `transport`, so one client can mix transports. Reconnect and backoff behave the same for all of them.

Errors, with `upstreams[<i>]: ` in front: `transport: "<value>" must be "tcp", "quic" or "kcp"`, `tls: must be enabled for transport "quic"`, `tls: not supported with transport "kcp"; set kcp.key to encrypt`.

### compression

Compression for the datagrams received from the server: `none` (default), `zstd` or `s2`, with the same two forms and the same `level` as the [server key](#compression). It must equal the `compression` of the server that the upstream dials. In the single-upstream shape it applies to the one upstream. In the multi-upstream shape it is the default for upstreams that omit `compression`; an upstream can set `compression: none` to turn it off, or its own algorithm and level. Errors carry the prefix `upstreams[<i>]: `.

When the settings differ, the client logs the mismatch and retries with its backoff:

| Situation | Client error |
| --- | --- |
| Client compresses, server uses another algorithm | `tunnel: compression mismatch between client and server: client uses zstd, server uses none` |
| Client uses `none`, server compresses | `tunnel: compression mismatch between client and server: client uses none, server requires another setting` |
| Client compresses, server is v0.2.0 or older | `tunnel: server does not support compression (protocol version 1)` |

The server logs `handshake rejected` with `status` 4, `client_compression` and `server_compression`.

### reconnect_backoff_min and reconnect_backoff_max

Delays between connection attempts for an upstream. After a failed attempt or a lost connection, that upstream waits, then tries again. The delay starts at `reconnect_backoff_min`, doubles after each failed attempt and stops growing at `reconnect_backoff_max`. A successful connection resets it to the minimum. Each upstream keeps its own delay.

| Key | Type | Default | Range |
| --- | --- | --- | --- |
| `reconnect_backoff_min` | duration | `1s` | positive |
| `reconnect_backoff_max` | duration | `30s` | not below `reconnect_backoff_min` |

Errors:

- `reconnect_backoff_min: must be positive`
- `reconnect_backoff_max: must not be below reconnect_backoff_min`
- `upstreams[<i>]: reconnect_backoff_min: must be positive`
- `upstreams[<i>]: reconnect_backoff_max: must not be below reconnect_backoff_min`

### stats_interval

Same as the [server key](#stats_interval). Default `10s`. One reporter covers every upstream.

### token

Shared secret presented during the handshake. At most 255 bytes. With `kcp` and no `kcp.key`, it is sent in clear text. In the single-upstream shape it is the tunnel token. In the multi-upstream shape it is the default inherited by an upstream that omits `token`. When the tokens differ, the server rejects the handshake and the client logs `tunnel: authentication failed` and retries that upstream.

### tls

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `tls.enabled` | boolean | `false` | Connect over TLS 1.2 or later (`tcp`); QUIC always uses TLS 1.3 and requires `true` |
| `tls.ca_file` | path | system roots | PEM file with the CA certificates that verify the server. When set, it replaces the system roots. |
| `tls.server_name` | string | host part of that upstream's `server` | Name expected in the server certificate |
| `tls.insecure_skip_verify` | boolean | `false` | Skip certificate verification. Use only for testing. |

In the multi-upstream shape, omit the upstream `tls` key to inherit the top-level block. Setting `tls:` on an upstream replaces the top-level block for that upstream. An upstream with `transport: kcp` that omits `tls` does not inherit an enabled top-level block, so a client can mix `tcp`/`quic` upstreams that use the top-level TLS with `kcp` ones. For `quic` the `ca_file`, `server_name` and `insecure_skip_verify` keys have the same meaning as for `tcp`.

Errors:

- `tls: options are set but tls.enabled is false`, when `ca_file`, `server_name` or `insecure_skip_verify` is set without `enabled: true`
- `upstreams[<i>]: tls: options are set but tls.enabled is false`
- `read tls ca_file: <reason>` at startup, when the file cannot be read
- `tls ca_file contains no certificates` at startup, when the file holds no PEM certificate

### kcp

Same keys as the [server `kcp` block](#kcp). `key`, `data_shards` and `parity_shards` must match the server. In the multi-upstream shape, an upstream that omits `kcp` inherits the top-level block; setting `kcp:` on an upstream replaces it as a whole. The block is validated only for upstreams whose transport is `kcp`; errors carry the prefix `upstreams[<i>]: kcp: `.

### channels

List of channels for the single-upstream shape. At least one is required. After normalize these become `upstreams[0].channels` with name `default`.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `id` | integer, 1 to 65535 | none | Channel ID. It must also exist in the server configuration. Must be unique within the upstream. |
| `name` | string | empty | Label used in stats log lines |
| `target` | `host:port` string | none | UDP address that receives the datagrams of this channel. Required. |

The client resolves each `target` once at startup and sends from one UDP socket per channel entry. If a client channel ID is missing from that upstream's server configuration, the server rejects the handshake and the client logs `tunnel: server does not know a requested channel`. Server channels that the client does not list are still sent; the client drops their datagrams and counts them as `unknown_channel` on that upstream.

Errors, where `<i>` is the position of the channel in the list (shown under `upstreams[0]` after normalize):

- `channels: at least one channel is required`
- `channels[<i>]: id must be non-zero`
- `channels[<i>]: id <id> already used by channels[<j>]`
- `channels[<i>]: target: <address message>`

### upstreams

List of tunnel servers to dial in parallel. Required for the multi-upstream shape; synthesized as one entry named `default` for the single-upstream shape.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `name` | string | none | Unique label for logs and stats (`upstream=<name>`). Required. |
| `server` | `host:port` | none | Address of that tunnel server. Required. |
| `transport` | string | top-level `transport` | `tcp`, `quic` or `kcp` |
| `compression` | string or object | top-level `compression` | `none`, `zstd` or `s2`, optionally with `level` |
| `token` | string | top-level `token` | Handshake token for this upstream |
| `tls` | object | top-level `tls` | TLS settings for this upstream. Omit the key to inherit (not inherited by `kcp` upstreams). |
| `kcp` | object | top-level `kcp` | KCP settings for this upstream. Omit the key to inherit. |
| `reconnect_backoff_min` | duration | top-level value | First reconnect delay for this upstream |
| `reconnect_backoff_max` | duration | top-level value | Longest reconnect delay for this upstream |
| `channels` | list | none | Channels for this upstream. At least one. Channel IDs are unique per upstream only. |

Each upstream keeps its own session (TCP, QUIC or KCP) and reconnect loop. Canceling the process stops every upstream.

Errors, where `<i>` is the upstream index and `<j>` a channel index:

- `upstreams: at least one upstream is required`
- `upstreams[<i>]: name is required`
- `upstreams[<i>]: name "<name>" already used by upstreams[<j>]`
- `upstreams[<i>]: server: <address message>`
- `upstreams[<i>]: token: longer than 255 bytes`
- `upstreams[<i>]: channels: ...` (same channel errors as above)

## Examples

A client that mixes transports and compression. `east` uses QUIC with the shared TLS block and zstd, `west` uses KCP with s2 and does not inherit that TLS block, and `lab` stays on TCP without compression:

```yaml
token: change-me
tls:
  enabled: true
  ca_file: /etc/udp-tcp-tunnel/ca.crt
kcp:
  key: change-me-too
upstreams:
  - name: east
    transport: quic
    compression: zstd              # must match east's server
    server: "192.168.1.10:9000"
    channels: [{id: 1, target: "127.0.0.1:2368"}]
  - name: west
    transport: kcp
    compression: {algorithm: s2, level: 2}
    server: "192.168.1.11:9000"
    channels: [{id: 1, target: "127.0.0.1:2369"}]
  - name: lab
    server: "192.168.1.12:9000"
    channels: [{id: 1, target: "127.0.0.1:2370"}]
```

## Example files

[examples/server.yaml](../examples/server.yaml), [examples/client.yaml](../examples/client.yaml) and [examples/client-multi.yaml](../examples/client-multi.yaml) are complete commented configurations for TCP. [examples/server-quic.yaml](../examples/server-quic.yaml) and [examples/client-quic.yaml](../examples/client-quic.yaml) use QUIC; [examples/server-kcp.yaml](../examples/server-kcp.yaml) and [examples/client-kcp.yaml](../examples/client-kcp.yaml) use KCP. [examples/client-full.yaml](../examples/client-full.yaml) is a kitchen-sink client with five upstreams that mix TCP, TCP+TLS, QUIC and KCP, with and without compression. The QUIC examples enable `zstd` and the KCP examples enable `s2`; the TCP examples show `compression: none` as a comment. Release archives include the matching example files next to each binary.
