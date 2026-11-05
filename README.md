# udp-tcp-tunnel

[![CI](https://github.com/nerdneilsfield/udp-tcp-tunnel/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/nerdneilsfield/udp-tcp-tunnel/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/nerdneilsfield/udp-tcp-tunnel?sort=semver)](https://github.com/nerdneilsfield/udp-tcp-tunnel/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

udp-tcp-tunnel carries UDP datagrams from one host to another over a single TCP, QUIC or KCP session and sends each datagram, byte for byte, to a local UDP address on the far side.

## Highlights

- One session carries many channels. A channel selects datagrams by destination port and, optionally, by source IP address and source port. One client process can dial several tunnel servers at once.
- Datagram boundaries are kept. Each datagram travels as one length-prefixed frame and leaves the client as one UDP datagram.
- On Linux, the server can capture packets with AF_PACKET while another program keeps receiving them on the same port.
- AF_PACKET capture uses a TPACKET_V3 ring from gopacket and a classic BPF filter generated in Go. It needs neither cgo nor libpcap.
- Three transports: `tcp` (default, optionally with TLS), `quic` (TLS 1.3, UDP payloads in QUIC datagrams) and `kcp` (KCP over UDP, optional encryption key and FEC). Each client upstream picks its own.
- Optional per-datagram compression with zstd or s2 on any transport. It is off by default, and a tunnel without it stays compatible with v0.2.0.
- A shared token and TLS 1.2 or later can protect the tunnel.
- Both sides log packet rates, byte counts and seven named drop counters for every channel at a fixed interval.
- The client is released for Linux, macOS and Windows. It does not link the capture code.

## Overview

The server (`udp-tcp-tunnel-server`) obtains UDP datagrams on the capture host, assigns each one to a channel and writes it to the session of the client. The client (`udp-tcp-tunnel-client`) connects to one or more servers, reads the frames and sends each payload from its own UDP socket to the `target` of that channel. Each upstream keeps its own session and reconnect loop.

The server obtains datagrams in one of two capture modes:

- `socket` binds the configured UDP ports itself. No other program can use those ports.
- `afpacket` copies matching packets from a network interface. The program that owns the port still receives them. This mode works only on Linux and needs `CAP_NET_RAW`.

udp-tcp-tunnel is not a VPN or a general port forwarder. Data flows in one direction only, from the server to the client, and replies from the target are not carried back. The original source address of a datagram is not preserved, IP fragments are not reassembled, and the server serves one client at a time.

The project is early and experimental. The first release is v0.1.0. The configuration format and the wire protocol can change between 0.x releases.

## Installation

### Release archives

Download archives from the [releases page](https://github.com/nerdneilsfield/udp-tcp-tunnel/releases). The archive names follow this pattern, where `<version>` has no leading `v`:

| Binary | Systems | Archive |
| --- | --- | --- |
| Server | Linux on amd64 and arm64 | `udp-tcp-tunnel-server_<version>_linux_<arch>.tar.gz` |
| Client | Linux and macOS on amd64 and arm64 | `udp-tcp-tunnel-client_<version>_<os>_<arch>.tar.gz` |
| Client | Windows on amd64 and arm64 | `udp-tcp-tunnel-client_<version>_windows_<arch>.zip` |

Each archive contains the binary, `README.md`, `LICENSE` and example configuration files at the top level (`server.yaml`, `server-quic.yaml` and `server-kcp.yaml` for the server; `client.yaml`, `client-multi.yaml`, `client-quic.yaml`, `client-kcp.yaml` and `client-full.yaml` for the client). Every release also has a `checksums.txt` file with SHA-256 checksums.

The following commands install v0.1.0 of both binaries on Linux amd64:

```sh
VERSION=0.1.0
BASE=https://github.com/nerdneilsfield/udp-tcp-tunnel/releases/download/v$VERSION
curl -LO "$BASE/udp-tcp-tunnel-server_${VERSION}_linux_amd64.tar.gz"
curl -LO "$BASE/udp-tcp-tunnel-client_${VERSION}_linux_amd64.tar.gz"
curl -LO "$BASE/checksums.txt"
sha256sum --ignore-missing -c checksums.txt
tar xzf "udp-tcp-tunnel-server_${VERSION}_linux_amd64.tar.gz" udp-tcp-tunnel-server
tar xzf "udp-tcp-tunnel-client_${VERSION}_linux_amd64.tar.gz" udp-tcp-tunnel-client
sudo install -m 0755 udp-tcp-tunnel-server udp-tcp-tunnel-client /usr/local/bin/
```

### go install

After v0.1.0 is published, Go 1.27.1 or later can build and install the binaries from source:

```sh
go install github.com/nerdneilsfield/udp-tcp-tunnel/cmd/udp-tcp-tunnel-server@v0.1.0
go install github.com/nerdneilsfield/udp-tcp-tunnel/cmd/udp-tcp-tunnel-client@v0.1.0
```

Binaries built this way report `dev` as their version, because the version string is set only by the release build.

### From a clone

```sh
git clone https://github.com/nerdneilsfield/udp-tcp-tunnel.git
cd udp-tcp-tunnel
make build
```

`make build` writes `udp-tcp-tunnel-server` and `udp-tcp-tunnel-client` to the repository root.

## Quick start

This example runs the server, the client and a UDP receiver on one Linux machine. It uses `socket` capture on loopback, so it needs no privileges. It needs `python3` and `nc` in addition to the two binaries.

Create a working directory with the two configuration files:

```sh
mkdir utt-demo && cd utt-demo
cat > server.yaml <<'YAML'
listen: "127.0.0.1:9000"
capture: socket
udp_bind: "127.0.0.1"
stats_interval: 5s
channels:
  - id: 1
    name: demo
    destination_port: 5000
YAML
cat > client.yaml <<'YAML'
server: "127.0.0.1:9000"
stats_interval: 5s
channels:
  - id: 1
    name: demo
    target: "127.0.0.1:6000"
YAML
```

The server reads datagrams sent to `127.0.0.1:5000`, and the client delivers them to `127.0.0.1:6000`.

In a first terminal, start a receiver on port 6000:

```sh
cat > recv.py <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("127.0.0.1", 6000))
while True:
    data, addr = s.recvfrom(65535)
    print(f"received {data!r} from {addr[0]}:{addr[1]}", flush=True)
PY
python3 recv.py
```

In a second terminal, start the server:

```sh
udp-tcp-tunnel-server -c server.yaml
```

In a third terminal, start the client:

```sh
udp-tcp-tunnel-client -c client.yaml
```

In a fourth terminal, send three datagrams to the server:

```sh
for i in 1 2 3; do printf "hello $i" | nc -u -w1 127.0.0.1 5000; done
```

The receiver prints the three payloads. The source address is the client's UDP socket, not the address of `nc`:

```text
received b'hello 1' from 127.0.0.1:55571
received b'hello 2' from 127.0.0.1:55571
received b'hello 3' from 127.0.0.1:55571
```

The server logs the connection, and after the next stats interval it logs three packets and 21 bytes on channel 1:

```text
2026-11-04T15:45:13.968-0500	INFO	server started	{"listen": "127.0.0.1:9000", "capture": "socket", "channels": 1}
2026-11-04T15:45:14.979-0500	INFO	client connected	{"remote": "127.0.0.1:57318"}
2026-11-04T15:45:18.973-0500	INFO	channel stats	{"side": "server", "channel": 1, "name": "demo", "pps": 0.6, "mbps": 0, "packets": 3, "bytes": 21, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
2026-11-04T15:45:18.974-0500	INFO	global drops	{"side": "server", "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
```

The client logs the same counts for its side:

```text
2026-11-04T15:45:14.979-0500	INFO	connected	{"server": "127.0.0.1:9000"}
2026-11-04T15:45:19.987-0500	INFO	channel stats	{"side": "client", "channel": 1, "name": "demo", "pps": 0.6, "mbps": 0, "packets": 3, "bytes": 21, "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
2026-11-04T15:45:19.987-0500	INFO	global drops	{"side": "client", "drop_queue_full": 0, "drop_unknown_channel": 0, "drop_oversized": 0, "drop_fragment": 0, "drop_capture_drop": 0, "drop_no_client": 0, "drop_write_error": 0}
```

Press Ctrl+C in each terminal to stop the programs. [docs/stats.md](docs/stats.md) explains every field.

## Usage

### Command line

Both binaries accept the same flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-c`, `--config <path>` | `server.yaml` for the server, `client.yaml` for the client | YAML configuration file |
| `-v`, `--verbose` | off | Log at debug level instead of info |
| `-h`, `--help` | | Print help |

The `version` subcommand prints the version, commit, build date, Go version and platform. The `completion` subcommand generates shell completion scripts.

Logs go to standard error. SIGINT and SIGTERM stop a binary cleanly. When configuration loading or startup fails, the binary prints `error: <message>` to standard error and exits with status 1. The configuration loader reports every validation problem in one message:

```text
error: invalid server config bad.yaml: interface: required when capture is "afpacket"
queue_size: must be at least 1
max_payload: must be between 1 and 65535
channels[0]: id must be non-zero
channels[0]: destination_port is required
```

### Channels and routing

Every channel has a numeric `id` between 1 and 65535 that both configuration files use. On the server, a channel matches datagrams by `destination_port` and, optionally, by `source_ip` and `source_port`. When several channels match one datagram, the most specific one wins:

1. `source_ip` and `source_port` set
2. only `source_ip` set
3. only `source_port` set
4. only `destination_port` set

Two channels cannot have the same match rule. A datagram that matches no channel is dropped and counted as `unknown_channel`.

The example configurations in [examples/](examples/) forward two LiDAR streams that share destination port 2368 and differ by source IP address.

### Capture next to an existing receiver

Use `afpacket` capture when another program on the server host must keep receiving the datagrams. The server then copies matching packets from the interface instead of binding the port.

1. Confirm with `tcpdump` that the packets reach the interface:

   ```sh
   sudo tcpdump -ni eth0 -Q in 'udp and dst port 2368'
   ```

2. Set the capture mode and the interface in `server.yaml`:

   ```yaml
   capture: afpacket
   interface: eth0
   ```

3. Give the server binary the `CAP_NET_RAW` capability, or run it as root:

   ```sh
   sudo setcap cap_net_raw+ep /usr/local/bin/udp-tcp-tunnel-server
   ```

Without the capability, startup fails with `afpacket capture on "eth0" (needs root or CAP_NET_RAW): ...`. Capabilities set with `setcap` are lost when the binary file is replaced, so repeat the command after each upgrade. [docs/capture-modes.md](docs/capture-modes.md) describes which packets the filter accepts.

### Choose a transport

`transport` selects how the server and the client talk: `tcp` (default), `quic` or `kcp`. Set it on the server and on every client upstream that connects to it. The client also accepts a top-level `transport` that upstreams inherit.

| Transport | Carried over | Encryption | Loss behavior |
| --- | --- | --- | --- |
| `tcp` | TCP, optional TLS 1.2+ | `tls` | Reliable, ordered; head-of-line blocking across channels |
| `quic` | UDP, QUIC datagrams | TLS 1.3, mandatory | Datagrams are not retransmitted; payloads above the datagram limit use the reliable stream |
| `kcp` | UDP | None unless `kcp.key` is set | Reliable, ordered like TCP, with faster retransmission; optional FEC |

`quic` and `kcp` use UDP, so open the server port for UDP in the firewall (for example `ufw allow 9000/udp`), not for TCP. See [examples/server-quic.yaml](examples/server-quic.yaml), [examples/client-quic.yaml](examples/client-quic.yaml), [examples/server-kcp.yaml](examples/server-kcp.yaml) and [examples/client-kcp.yaml](examples/client-kcp.yaml). The wire details are in [docs/protocol.md](docs/protocol.md).

KCP sends the token and all data in clear text unless `kcp.key` is set on both sides. The key gives confidentiality only: KCP does not authenticate packets. Use `quic` on an untrusted network.

### Compress the payloads

`compression` shrinks each datagram before it crosses the tunnel. It helps when the payloads are compressible and the link is the bottleneck, for example text-like or sparse sensor data; encrypted or already compressed payloads gain nothing.

```yaml
# server.yaml and client.yaml: both ends must agree
compression: zstd          # none (default), zstd or s2
```

Use the mapping form to set a level, `compression: {algorithm: zstd, level: 3}`. Each datagram is compressed on its own, so packet loss on `quic` or `kcp` never breaks later datagrams, and a datagram that does not shrink is sent as it is. If the ends differ, the handshake fails with a `compression mismatch` error. A client with compression `none` still works with a v0.2.0 server. See [docs/configuration.md](docs/configuration.md#compression) and [docs/protocol.md](docs/protocol.md#compression). Ratios and counters appear in the [`compression stats`](docs/stats.md#compression-stats) log line.

### Protect the tunnel

Without TLS, the token and all data cross the network in clear text. When the server listens on an address that is not loopback and TLS or the token is missing, it logs this warning at startup:

```text
2026-11-04T15:45:37.884-0500	WARN	tunnel listens on a non-loopback address without full protection	{"listen": "[::]:19000", "tls": false, "token": true}
```

To protect a tunnel that crosses an untrusted network, set the same `token` on both sides and enable TLS:

```yaml
# server.yaml
token: <shared-secret>
tls:
  enabled: true
  cert_file: /etc/udp-tcp-tunnel/server.crt
  key_file: /etc/udp-tcp-tunnel/server.key
```

```yaml
# client.yaml
token: <shared-secret>
tls:
  enabled: true
  ca_file: /etc/udp-tcp-tunnel/ca.crt
```

The client verifies the server certificate against `ca_file`, or against the system roots when `ca_file` is not set. It expects the certificate to match the host part of `server` unless `server_name` is set.

## Configuration

The keys that most setups change are listed below. [docs/configuration.md](docs/configuration.md) describes every key, its validation rules and its error messages.

Server (`server.yaml`):

| Key | Default | Meaning |
| --- | --- | --- |
| `listen` | `:9000` | Address for the client connection (TCP or UDP, by `transport`) |
| `transport` | `tcp` | `tcp`, `quic` or `kcp` |
| `capture` | `socket` | `socket` or `afpacket` |
| `udp_bind` | `0.0.0.0` | Address for the UDP ports in `socket` mode |
| `interface` | none | Interface to capture on in `afpacket` mode |
| `queue_size` | `1024` | Datagrams that can wait for the TCP writer |
| `stats_interval` | `10s` | Interval between stats log lines |
| `token` | empty | Shared secret that the client must present |
| `tls` | off | Certificate and key; mandatory for `quic`, not allowed for `kcp` |
| `kcp` | defaults | Tuning, FEC and `key` for `transport: kcp` |
| `compression` | `none` | `none`, `zstd` or `s2`; must match the client |
| `channels` | none | At least one channel with `id` and `destination_port` |

Client (`client.yaml`):

| Key | Default | Meaning |
| --- | --- | --- |
| `server` | none | Server address as `host:port` (single-upstream shape) |
| `transport` | `tcp` | `tcp`, `quic` or `kcp`; upstreams inherit it |
| `upstreams` | none | List of named servers to dial in parallel (multi-upstream shape; mutually exclusive with `server`/`channels`) |
| `reconnect_backoff_min` | `1s` | First delay before a reconnect |
| `reconnect_backoff_max` | `30s` | Longest delay before a reconnect |
| `stats_interval` | `10s` | Interval between stats log lines |
| `token` | empty | Default tunnel token; upstreams may override |
| `tls` | off | Default TLS settings; upstreams may override |
| `kcp` | defaults | Default KCP settings; upstreams may override |
| `compression` | `none` | Default `none`, `zstd` or `s2`; upstreams may override |
| `channels` | none | Channels for the single-upstream shape |

Use [examples/client-multi.yaml](examples/client-multi.yaml) when one process should dial several servers. Channel IDs are unique per upstream only. For a single reference that mixes TCP, QUIC, KCP and compression across five upstreams, see [examples/client-full.yaml](examples/client-full.yaml).

Unknown keys are rejected, so a misspelled key stops the program at startup.

## Caveats

- **The source address is not preserved.** The receiver sees every datagram of a channel coming from the client host, from one UDP port that the client picks when it starts. A receiver that filters by sender address must be configured for the client address.
- **A vanished TCP peer used to stall for minutes.** Writes can succeed into the kernel send buffer while retransmits run under `tcp_retries2` (often ~8–15 minutes). The tunnel now sets Linux `TCP_USER_TIMEOUT` to 30s and short keepalive probes so the session fails fast and the client reconnects.
- **QUIC may lose and reorder datagrams.** QUIC datagrams are unreliable, so a datagram lost on the network is not retransmitted, and a payload above the path's datagram limit (about 1200 bytes at first, up to about 1450 after path MTU discovery) is sent on the reliable stream, which can arrive out of order relative to datagrams. Neither case is counted in the stats.
- **Compression costs CPU and can leak sizes.** Compressed sizes depend on the content, so on an encrypted tunnel an observer can learn something about the payloads from packet sizes. Leave compression off when that matters. Datagram boundaries and content are always preserved.
- **KCP is unencrypted by default.** Without `kcp.key` the token and the data are readable on the path. With a key they are encrypted but not authenticated.
- **TCP adds head-of-line blocking.** All channels share one TCP connection. A lost TCP segment delays every channel until it is retransmitted. When the connection cannot keep up, the server queue fills and the newest datagrams are dropped as `queue_full`.
- **macOS limits outgoing datagram size.** macOS caps a UDP datagram at the `net.inet.udp.maxdgram` sysctl, which is 9216 bytes by default. A client on macOS cannot send a larger payload and counts it as `write_error`. Raise the limit with `sudo sysctl -w net.inet.udp.maxdgram=65535` if the tunnel carries larger datagrams.
- **IP fragments are not reassembled.** In `afpacket` mode the server counts IPv4 first fragments as `fragment` and drops them. The kernel filter drops later IPv4 fragments and all IPv6 fragments without counting them. In `socket` mode the kernel reassembles fragments before the server reads them.
- **The server serves one client.** A new client connection replaces the current one. Datagrams captured while no client is connected are dropped as `no_client`, and datagrams still queued when a client connects are discarded, so a client never receives old data. One client process may still dial many servers; each server still accepts only one client session.
- **Targets are resolved once.** The client resolves each `target` host name when it starts. Restart the client after a target address changes.
- **AF_PACKET capture is tested only at the unit level.** The live capture test runs only as root, and CI runs the tests without root.

## Documentation

- [docs/configuration.md](docs/configuration.md): every configuration key, default and validation error
- [docs/capture-modes.md](docs/capture-modes.md): `socket` and `afpacket` capture, the kernel filter and privileges
- [docs/protocol.md](docs/protocol.md): the handshake and frame format, and how each transport carries them
- [docs/stats.md](docs/stats.md): the stats log lines and the drop counters

## Contributing

Report bugs and propose changes through [GitHub issues](https://github.com/nerdneilsfield/udp-tcp-tunnel/issues) and pull requests. Commit messages must follow [Conventional Commits](https://www.conventionalcommits.org/); the `commit-msg` hook installed by `make hooks` checks them. Run `make check` before you open a pull request.

## License

udp-tcp-tunnel is released under the MIT License. See [LICENSE](LICENSE).

## Development

The project needs Go 1.27.1 or later. `make check` also needs [golangci-lint](https://golangci-lint.run/) and [GoReleaser](https://goreleaser.com/), which are installed separately; CI uses golangci-lint v2.15.0 and GoReleaser v2.19.0. gofumpt, goimports and lefthook are Go tool dependencies and need no separate installation.

| Command | Action |
| --- | --- |
| `make build` | Build both binaries into the repository root |
| `make cross-build` | Compile every release target without writing binaries |
| `make run-server`, `make run-client` | Run a binary with `examples/server.yaml` or `examples/client.yaml`, or with `CONFIG=<path>` |
| `make test` | Run the tests with coverage. It runs `make clean` first, which deletes `dist/` and built binaries |
| `make fmt`, `make fmt-check` | Format the code, or check the formatting |
| `make vet`, `make lint` | Run `go vet` or golangci-lint |
| `make check` | Run `fmt-check`, `lint`, `vet`, `test` and `release-check` |
| `make release-check` | Validate `.goreleaser.yml` |
| `make release-test` | Build a snapshot release into `dist/` |
| `make hooks` | Install the lefthook Git hooks |

The `justfile` provides the same tasks for [just](https://github.com/casey/just). CI runs `go test -race ./...`, which `make test` does not.

The code is organized as follows:

| Path | Contents |
| --- | --- |
| `cmd/udp-tcp-tunnel-server`, `cmd/udp-tcp-tunnel-client` | Entry points |
| `internal/cli` | Command line and signal handling shared by both binaries |
| `internal/logging` | Logger setup |
| `internal/buildinfo` | Version, commit and build date set at link time |
| `internal/config` | Configuration loading, defaults and validation |
| `internal/capture` | `socket` and `afpacket` capture, BPF filter and packet parser |
| `internal/routing` | Channel matching |
| `internal/server` | Queue, client session and drop accounting on the server |
| `internal/tunnel` | Wire protocol, TCP/TLS, QUIC and KCP transports, client |
| `internal/stats` | Counters and stats logging |

The transport is defined by the `Session`, `Listener` and `Dialer` interfaces in `internal/tunnel/transport.go`, so a transport can be added without changing the server and client logic. TCP, KCP and QUIC share the handshake in `handshake.go` and the codecs in `compress.go`; TCP and KCP also share the stream session in `session.go`.
