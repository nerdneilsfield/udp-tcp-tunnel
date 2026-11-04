# Wire protocol

This page describes the bytes that the server and the client exchange over the TCP connection. The definitions are in `internal/tunnel/protocol.go` and `internal/tunnel/tcp.go`. All integers are unsigned and big-endian.

The protocol has version 1 and the magic value `UTT1` (bytes `55 54 54 31`). It can change between 0.x releases; the version byte in the handshake lets each side detect a mismatch.

## Connection

The client opens a TCP connection to `server`. When TLS is enabled on both sides, the TLS handshake runs first and every message below travels inside TLS. Both sides enable `TCP_NODELAY` and TCP keepalive with a 30-second period.

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

## Session rules

- The server keeps one active client. When a second client completes the handshake, the server closes the first connection.
- When a client is accepted, the server discards datagrams still waiting in its queue, so the client receives only datagrams captured after it connected.
- The client detects a closed connection when a read fails. A silent network failure is detected through TCP keepalive.

## Other transports

The server and the client use the transport through the `Session`, `Listener` and `Dialer` interfaces in `internal/tunnel/transport.go`. TCP is the only implementation.
