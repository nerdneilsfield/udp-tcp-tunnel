# udp-tcp-tunnel

Carry several UDP streams (for example the data ports of a few lidars) over **one TCP connection**, and re-emit each datagram unchanged on another machine.

Two binaries are built from this repository:

| Binary | Platforms | Role |
| --- | --- | --- |
| `udp-tcp-tunnel-server` | Linux (amd64, arm64) | Runs next to the sensors. Captures UDP datagrams, labels them with a channel id and sends them over TCP. |
| `udp-tcp-tunnel-client` | Linux, macOS, Windows (amd64, arm64) | Runs on your computer. Dials the server, reads frames and sends each payload as a normal UDP packet to a local target. |

The client does not link any Linux-only or packet-capture code (CI checks this).

## How it works

```
 lidar .201 ──UDP :2368──┐                                         ┌─► 127.0.0.1:2368  (channel 1)
                         ├─► udp-tcp-tunnel-server ══ one TCP ══►  udp-tcp-tunnel-client
 lidar .202 ──UDP :2368──┘     (capture + router)   (TLS optional)  └─► 127.0.0.1:2369  (channel 2)
```

1. The server obtains each UDP datagram (see [capture modes](#capture-modes)).
2. The router maps it to a `channel_id` by *(source IP, optional source port, destination port)*. Datagrams that match no channel are dropped and counted.
3. The datagram goes into a bounded queue; a single writer sends it to the connected client.
4. The client looks up the channel's local target and sends the payload as one UDP datagram. Payload bytes and datagram boundaries are identical to the original.

The server is the TCP listener. The client dials it and reconnects automatically with exponential backoff. While no client is connected the server **drops** packets instead of buffering old data.

### Caveats

- **The original source IP and port are not preserved.** The receiving application sees packets coming from the client machine. Applications that filter on the sensor's IP (some lidar drivers do) need to be configured accordingly.
- **TCP head-of-line blocking.** One lost TCP segment delays every channel behind it, so latency spikes after packet loss and old data is delivered late rather than dropped. If the link cannot keep up, the server queue fills and new packets are dropped (counted as `queue_full`).
- IP fragments are not reassembled in `afpacket` mode; they are counted and ignored.

## Capture modes

Set `capture` in the server config.

### `socket` (default)

The server binds ordinary UDP sockets on `udp_bind` for every distinct `destination_port` in `channels`. Works everywhere, needs no privileges (except for ports below 1024), uses only the standard library.
Use it when **nothing else owns the port**.

### `afpacket` (Linux only)

The server sniffs packets passively with an AF_PACKET socket and a TPACKET_V3 mmap ring (`github.com/gopacket/gopacket/afpacket`, no cgo, no libpcap). A kernel BPF filter built in pure Go selects `udp and dst port N [and src host H] [and src port S]` for untagged and 802.1Q-tagged IPv4/IPv6 traffic and ignores frames the host itself sent. The real receiver on the same port keeps getting its packets.
Use it when **another program already owns the port** and must keep receiving. It needs `CAP_NET_RAW` (or root):

```sh
sudo setcap cap_net_raw+ep ./udp-tcp-tunnel-server
```

Check that the traffic is visible on the interface before using it:

```sh
sudo tcpdump -ni eth0 -Q in 'udp and dst port 2368'
```

Notes:

- Payloads are copied out of the ring before they are queued, because the ring slot is reused after the next read.
- Frames larger than the ring frame size (4 KiB by default, so jumbo frames) are truncated and skipped.
- If the kernel ring overflows the drops are reported as `capture_drop`.
- VLAN tags stripped by NIC offload are invisible to BPF; such packets are treated as untagged and still match.

## Configuration

Both files are YAML; unknown keys are rejected. Annotated examples: [`examples/server.yaml`](examples/server.yaml) and [`examples/client.yaml`](examples/client.yaml).

### Server

| Key | Default | Description |
| --- | --- | --- |
| `listen` | `:9000` | TCP address clients connect to. |
| `capture` | `socket` | `socket` or `afpacket`. |
| `udp_bind` | `0.0.0.0` | `socket` mode: local IP to bind the UDP ports on. |
| `interface` | – | `afpacket` mode: interface to sniff (required). |
| `queue_size` | `1024` | Datagrams waiting for the TCP writer. When full, the **newest** datagram is dropped. |
| `max_payload` | `65535` | Larger payloads are dropped (`oversized`). Max 65535, the protocol limit. |
| `stats_interval` | `10s` | Period of the stats log lines. |
| `token` | – | Shared secret (max 255 bytes). |
| `tls.enabled`, `tls.cert_file`, `tls.key_file` | off | Serve the tunnel over TLS (1.2+). |
| `channels[]` | – | At least one. `id` (non-zero, unique), `name`, `source_ip`, `source_port`, `destination_port` (required). |

A packet belongs to the most specific matching channel: source IP + port, then source IP, then source port, then destination port only. Identical rules are rejected.

### Client

| Key | Default | Description |
| --- | --- | --- |
| `server` | – | `host:port` of the server (required). |
| `reconnect_backoff_min` / `_max` | `1s` / `30s` | Retry delay starts at the minimum, doubles per failure up to the maximum. |
| `stats_interval` | `10s` | Period of the stats log lines. |
| `token` | – | Must equal the server's token. |
| `tls.enabled` | off | Use TLS. |
| `tls.ca_file` | system roots | CA bundle that signed the server certificate. |
| `tls.server_name` | host of `server` | Name to verify. |
| `tls.insecure_skip_verify` | `false` | Disable verification (testing only). |
| `channels[]` | – | `id` (must exist on the server), `name`, `target` (`host:port`). |

### TLS and token

The token is checked in the handshake with a constant-time compare, and the server also verifies that every channel id the client asks for exists. Without TLS the token travels in clear text: use TLS (and keep the listener on a trusted network) for anything exposed. The server logs a warning when it listens on a non-loopback address without both TLS and a token.

Quick self-signed pair for testing:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 365 \
  -keyout server.key -out server.crt -subj "/CN=tunnel.example.com" \
  -addext "subjectAltName=DNS:tunnel.example.com,IP:192.168.1.10"
```

Use `server.crt` as the client's `tls.ca_file`.

## Wire protocol

Implemented in [`internal/tunnel/protocol.go`](internal/tunnel/protocol.go).

```
client → server  "UTT1" | version u8 | token_len u8 | token | n u16 | n × channel_id u16
server → client  "UTT1" | version u8 | status u8      (0 ok, 1 bad version, 2 auth failed, 3 unknown channel)
server → client  channel_id u16 BE | payload_length u32 BE | payload      (repeated)
```

Payloads longer than 65535 bytes are rejected. TCP has `TCP_NODELAY` and keepalive enabled. The transport sits behind the `Session`, `Listener` and `Dialer` interfaces in `internal/tunnel/transport.go`, so another transport (e.g. QUIC datagrams) can replace TCP.

## Stats

Every `stats_interval` both binaries log one line per channel and one line of unattributed drops:

```
channel stats  side=server channel=1 name=lidar-201 pps=1850.2 mbps=17.9 packets=… bytes=…
               drop_queue_full=0 drop_oversized=0 drop_no_client=0 drop_write_error=0 …
global drops   side=server drop_unknown_channel=12 drop_fragment=0 drop_capture_drop=0
```

| Reason | Meaning |
| --- | --- |
| `queue_full` | Send queue full; newest packet dropped (per channel). |
| `no_client` | No client connected (per channel). |
| `oversized` | Payload above `max_payload` (per channel). |
| `write_error` | TCP send failed (server) or local UDP send failed (client). |
| `unknown_channel` | No channel matched (server) or unknown id received (client). Global. |
| `fragment` | IP fragment seen in `afpacket` mode. Global. |
| `capture_drop` | Packets dropped by the AF_PACKET ring in the kernel. Global. |

`packets`/`bytes` count payloads actually sent (server) or delivered (client).

## Build, run, test

```sh
make build            # udp-tcp-tunnel-server and udp-tcp-tunnel-client in the repo root
make cross-build      # compile every release target
make check            # fmt-check, lint, vet, test, goreleaser check

./udp-tcp-tunnel-server -c examples/server.yaml
./udp-tcp-tunnel-client -c examples/client.yaml
./udp-tcp-tunnel-client version
```

`just` offers the same targets. Binaries are built with `CGO_ENABLED=0`. `go test -race ./...` needs no privileges; `TestAFPacketLive` is skipped unless the process has `CAP_NET_RAW`:

```sh
go test -c -o capture.test ./internal/capture && sudo ./capture.test -test.run AFPacketLive -test.v
```

Layout:

```
cmd/udp-tcp-tunnel-server/   server main
cmd/udp-tcp-tunnel-client/   client main
internal/cli/                shared cobra root + version command
internal/config/             YAML structs, defaults, validation
internal/capture/            socket capture, AF_PACKET capture, packet parser, BPF filter
internal/routing/            packet → channel matching
internal/server/             capture → queue → transport
internal/tunnel/             protocol, transport interfaces, TCP/TLS, client
internal/stats/              per-channel counters and periodic logging
internal/buildinfo/          version, commit, date injected with -ldflags
internal/logging/            zap logger writing to stderr
```

Tooling: gofumpt, goimports and lefthook are `tool` dependencies (`make fmt`, `make hooks`); golangci-lint and goreleaser are installed separately. The Git hooks check staged Go files and enforce Conventional Commits.

## Release

Push a `v*` tag. The release workflow runs GoReleaser, which publishes a GitHub Release with separate archives per binary (`udp-tcp-tunnel-server_<version>_linux_<arch>.tar.gz`, `udp-tcp-tunnel-client_<version>_<os>_<arch>.tar.gz`, `.zip` on Windows) plus `checksums.txt`, then attests the checksummed artifacts. No container images are published. Try it locally with `make release-test`.
