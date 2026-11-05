# Wire protocol

This page describes the bytes that the server and the client exchange, and how each transport carries them. The definitions are in `internal/tunnel/protocol.go`, `handshake.go`, `session.go`, `tcp.go`, `kcp.go` and `quic.go`. All integers are unsigned and big-endian.

The protocol has version 1 and the magic value `UTT1` (bytes `55 54 54 31`). It can change between 0.x releases; the version byte in the handshake lets each side detect a mismatch.

## Transports

The hello, the reply and the frames below are the same for every transport. The transport only decides which byte stream or message channel carries them.

| Transport | Hello and reply | Frames | Dead peer |
| --- | --- | --- | --- |
| `tcp` | The TCP (or TLS) connection | The same connection, as a byte stream | Keepalive and `TCP_USER_TIMEOUT`, about 30s |
| `kcp` | The KCP session, used as a byte stream | The same session, as a byte stream | No frame for 30s |
| `quic` | The first bidirectional stream, opened by the client | QUIC DATAGRAM frames; oversized payloads use the first stream | QUIC idle timeout, 30s |

The sections "Connection" to "Frames" describe TCP. The sections [KCP](#kcp) and [QUIC](#quic) give the differences.

## Connection

The client opens a TCP connection to `server`. When TLS is enabled on both sides, the TLS handshake runs first and every message below travels inside TLS. Both sides enable `TCP_NODELAY`, TCP keepalive (idle 15s, interval 5s, 3 probes) and, on Linux, `TCP_USER_TIMEOUT` of 30 seconds.

A connection has two phases:

1. The client sends a hello and the server answers with a reply.
2. After a successful reply, only the server sends data, as a stream of frames. The client sends nothing further.

## Hello

The client sends one hello immediately after connecting:

| Field | Size | Value |
| --- | --- | --- |
| magic | 4 bytes | `UTT1` |
| version | 1 byte | `1` |
| token_len | 1 byte | Length of the token, 0 to 255 |
| token | token_len bytes | The `token` from the client configuration |
| n_channels | 2 bytes | Number of channel IDs that follow |
| channel_id | 2 bytes each | The IDs of the client channels |

A hello with an empty token and channel 1 is 10 bytes:

```text
55 54 54 31 01 00 00 01 00 01
```

## Reply

The server checks the hello and sends one reply:

| Field | Size | Value |
| --- | --- | --- |
| magic | 4 bytes | `UTT1` |
| version | 1 byte | `1` |
| status | 1 byte | See the status table |

| Status | Name | Meaning | Client error |
| --- | --- | --- | --- |
| 0 | OK | The client is accepted | |
| 1 | BadVersion | The hello has an unsupported version | `tunnel: unsupported protocol version` |
| 2 | AuthFailed | The token differs from the server token | `tunnel: authentication failed` |
| 3 | UnknownChannel | A requested channel ID is not in the server configuration | `tunnel: server does not know a requested channel` |

The server compares the tokens in constant time, then checks the channel IDs. After any status other than 0, the server closes the connection. When the hello does not start with `UTT1`, or is incomplete, the server closes the connection without a reply. A client that receives a reply without the magic value reports `tunnel: bad handshake magic`, and a status outside the table produces `tunnel: unknown handshake status <n>`.

The replies below were captured from a running server:

```text
OK:             55 54 54 31 01 00
UnknownChannel: 55 54 54 31 01 03
BadVersion:     55 54 54 31 01 01
```

The hello and the reply must complete within 10 seconds of the connection being accepted, including the TLS handshake. The client applies a 10-second limit to the TCP connect and another to its side of the handshake. When the handshake fails, the server logs `handshake failed` or `handshake rejected` with the remote address and the status:

```text
2026-11-04T15:45:38.397-0500	WARN	handshake rejected	{"remote": "127.0.0.1:57550", "status": 2}
2026-11-04T15:48:14.970-0500	WARN	handshake failed	{"remote": "127.0.0.1:60694", "error": "tunnel: unsupported protocol version"}
```

## Frames

After the reply, the server sends one frame for each datagram:

| Field | Size | Value |
| --- | --- | --- |
| channel_id | 2 bytes | Channel of the datagram |
| payload_length | 4 bytes | Payload length in bytes, 0 to 65535 |
| payload | payload_length bytes | The UDP payload, unchanged |

The frame header is 6 bytes. A frame for the payload `hello 1` on channel 1, captured from a running server:

```text
00 01 00 00 00 07 68 65 6c 6c 6f 20 31
```

The receiver rejects a frame whose length field exceeds 65535 before it reads the payload, and closes the connection. The server sends frames for every configured channel, including channels that the client did not list in its hello. The client drops frames for channels it does not know and counts them as `unknown_channel`.

Each frame write has a 15-second deadline. If a write fails or times out, the server counts the datagram as `write_error`, logs `send failed` and closes the connection. The client then reconnects with its backoff.

A write deadline alone is not enough when the peer disappears while the TCP send buffer still has room: `Write` copies into the kernel and returns success, and the connection only fails after `tcp_retries2` (often around 8–15 minutes). Linux `TCP_USER_TIMEOUT` of 30 seconds aborts such connections when data stays unacknowledged. Keepalive covers the idle case (about 30 seconds with the settings above).

## Session rules

- The server keeps one active client. When a second client completes the handshake, the server closes the first connection and the previous serve loop exits so it does not share the queue with the new client.
- When a client is accepted, the server discards datagrams still waiting in its queue, so the client receives only datagrams captured after it connected.
- The client detects a closed connection when a read fails. A silent network failure is detected through TCP keepalive when idle, and through `TCP_USER_TIMEOUT` when data is in flight. QUIC and KCP detect it within 30 seconds as described below.

## KCP

The client opens a KCP session (kcp-go, v5) to the UDP port in `server`. KCP has no connection setup, so the server first sees the session when the client sends the hello, and a client that cannot reach the server finds out when the 10-second handshake times out. The session runs in stream mode, so the hello, the reply and the frames are byte-for-byte the same as on TCP, read through the same code.

Both ends configure the session from `kcp` (see [configuration.md](configuration.md#kcp)): nodelay, update interval, fast resend, congestion control, send and receive window and MTU. The defaults are `nodelay=1, interval=20ms, resend=2, nc=1`, windows of 1024 packets and MTU 1350.

`kcp.key` and the FEC shard counts change the packet format and must be equal on both ends:

- With a key, every UDP packet is encrypted with AES-256. The key is `PBKDF2-HMAC-SHA256(kcp.key, salt "udp-tcp-tunnel kcp v1", 100000 iterations, 32 bytes)`. A client with a different key does not produce a valid session; the server drops its packets and the client's handshake times out.
- Without a key nothing is encrypted, including the token.
- With `data_shards` and `parity_shards`, kcp-go adds Reed-Solomon forward error correction.
- KCP does not authenticate packets. The token authenticates the client only at the handshake.

Unlike TCP, KCP has no keepalive and does not tell the peer when a session closes. Both ends therefore send a **heartbeat** frame every 10 seconds: channel 0, payload length 0, so six bytes `00 00 00 00 00 00`. Channel 0 is reserved (configured channel IDs start at 1). A receiver drops heartbeat frames without counting them. The receiver closes the session when no frame, heartbeat or data, has arrived for 30 seconds, and the client then reconnects with its backoff. The server also uses this to notice a client that vanished. TCP and QUIC do not send heartbeat frames.

A server that rejects the hello keeps the session open for up to 2 seconds so that the reply can be delivered, then closes it. The hello and the reply must complete within 10 seconds.

## QUIC

The client opens a QUIC connection (quic-go) to the UDP port in `server` with TLS 1.3 and the ALPN `udp-tcp-tunnel/1`. A server or client that offers another ALPN fails the TLS handshake. TLS is mandatory: the server uses `tls.cert_file` and `tls.key_file`, the client verifies the certificate as for TCP (`ca_file`, `server_name`, `insecure_skip_verify`). Both ends enable QUIC datagrams (RFC 9221).

After the QUIC handshake the client opens the first bidirectional stream and sends the hello on it. The server answers with the reply on the same stream. The stream stays open for the life of the connection. The hello and the reply must complete within 10 seconds. After a rejection the server waits up to 2 seconds for the client to close, then closes the connection with application error code 1.

Frames travel in two ways:

- **Datagram (normal case).** Each UDP payload is sent as one QUIC DATAGRAM frame: `channel_id` (2 bytes) followed by the payload, with no length field. The datagram is unreliable and unordered. The network can lose or reorder it, and QUIC does not retransmit it.
- **Stream (fallback).** When `channel_id` plus the payload does not fit in the connection's current maximum datagram size, the server writes the payload as an ordinary frame (the TCP frame format, with header) on the first stream. It arrives reliably, but not in order relative to datagrams. The datagram limit starts near 1200 bytes and grows with path MTU discovery, up to about 1450 bytes on Ethernet. Dropping instead would lose every datagram larger than the limit, for example a 1206-byte lidar packet before discovery finishes, so the tunnel delivers it late rather than not at all. Neither path is counted in the stats.

Dead peers: the idle timeout is 30 seconds, and each end sends a keepalive packet every 10 seconds. A connection that receives nothing for 30 seconds is closed on both ends, the client reconnects with its backoff, and the server then accepts the new connection. Closing a session sends CONNECTION_CLOSE, so a clean shutdown is seen immediately. The server sets the stream limit to 1 and does not accept unidirectional streams.

## Other transports

The server and the client use a transport through the `Session`, `Listener` and `Dialer` interfaces in `internal/tunnel/transport.go`. The hello and reply logic (`handshake.go`) is shared by all three transports, and TCP and KCP share one stream session (`session.go`).
