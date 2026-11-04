# Capture modes

The server obtains UDP datagrams in one of two modes, selected by the `capture` key. This page explains how each mode works, what it needs and which datagrams it can miss.

| | `socket` | `afpacket` |
| --- | --- | --- |
| Systems | Any system the server runs on | Linux only |
| Privileges | None for ports above 1023 | Root or `CAP_NET_RAW` |
| Port ownership | The server binds the ports. No other program can bind them. | The server binds nothing. The program that owns the port keeps receiving. |
| IP fragments | Reassembled by the kernel | Not reassembled. See [Fragments](#fragments). |
| `capture_drop` counter | Always 0 | Kernel ring drops |
| `fragment` counter | Always 0 | IPv4 first fragments |

## socket

In `socket` mode the server opens one UDP socket for each distinct `destination_port` in the channel list and binds it to `udp_bind`. Channels that share a destination port share the socket and are told apart by source address and port.

Each socket reads datagrams of up to 65535 bytes. The server requests a 4 MiB receive buffer for each socket. The request is best effort: Linux caps it at the `net.core.rmem_max` sysctl.

If a port is already in use, the server stops at startup. This is the error for a server with `udp_bind: "127.0.0.1"` and a channel on port 5000:

```text
error: socket capture: listen udp 127.0.0.1:5000: listen udp 127.0.0.1:5000: bind: address already in use
```

Packets that the kernel drops because a socket buffer is full are not visible to the server, so `socket` mode reports no kernel drops.

## afpacket

In `afpacket` mode the server opens an AF_PACKET socket on `interface` and reads copies of the received frames. The kernel still delivers the original packets to whatever program owns the port, so the server can run next to an existing receiver. This mode needs no libpcap and no cgo.

### Privileges

Opening the socket needs root or the `CAP_NET_RAW` capability. To grant the capability to the binary:

```sh
sudo setcap cap_net_raw+ep /usr/local/bin/udp-tcp-tunnel-server
```

`setcap` stores the capability on the file, so it must be set again after the binary is replaced. Without the capability, startup fails with this error:

```text
error: afpacket capture on "eth0" (needs root or CAP_NET_RAW): <reason>
```

On systems other than Linux, `afpacket` mode fails with `afpacket capture is only supported on Linux`.

### Check the traffic first

Before you configure the server, confirm that the packets reach the interface with the expected addresses and ports:

```sh
sudo tcpdump -ni eth0 -Q in 'udp and dst port 2368'
```

`-Q in` limits `tcpdump` to received packets, which is also what the server captures.

### Ring buffer

The server uses a TPACKET_V3 memory-mapped ring with the default sizes of the gopacket `afpacket` package: 128 blocks of 512 KiB, which is 64 MiB per server. Each read waits at most 200 ms, so the server notices a shutdown request quickly. When the ring is full, the kernel drops frames. The server reads the kernel drop counter at every stats interval and reports the total since startup as the global `capture_drop` counter.

### Kernel filter

The server attaches a classic BPF filter to the socket, so the kernel discards unwanted frames before they reach the ring. The filter is generated from the channel list. For each channel it is equivalent to this `tcpdump` expression, and a frame is accepted when it matches any channel:

```text
udp and dst port <destination_port> [and src host <source_ip>] [and src port <source_port>]
```

A channel with an IPv4 `source_ip` matches only IPv4 packets, and a channel with an IPv6 `source_ip` matches only IPv6 packets. A channel without `source_ip` matches both.

The filter also applies these rules to the frame bytes it sees:

| Frame | Result |
| --- | --- |
| Sent by this host (packet type `PACKET_OUTGOING`) | Rejected |
| Ethernet with EtherType IPv4 or IPv6 | Checked against the channels |
| One 802.1Q tag (EtherType 0x8100) followed by IPv4 or IPv6 | Checked against the channels |
| Any other EtherType, including 802.1ad (0x88a8) | Rejected |
| IPv4 with a protocol other than UDP | Rejected |
| IPv4 fragment with a non-zero fragment offset | Rejected |
| IPv6 whose next header is not UDP, including any extension header | Rejected |

Accepted frames are copied to the ring in full, up to 65535 bytes. A filter with too many channels cannot be built and the server stops with `afpacket capture: bpf: too many capture rules for one filter`.

The Linux kernel can move the 802.1Q tag of a received frame into packet metadata, either in the network card or in software. The filter and the parser then see an untagged frame, and the packet matches by address and port whatever its VLAN. The server does not ask gopacket to restore the tag. Capture on VLAN traffic has not been tested.

### Parser

The server parses every accepted frame in user space. The parser reads the Ethernet header, any 802.1Q or 802.1ad tags, the IPv4 or IPv6 header and the UDP header. It removes Ethernet padding by using the IPv4 total length. For IPv6 it skips hop-by-hop, routing and destination options headers, although the kernel filter has already rejected packets that carry them.

A frame that the parser cannot read completely, for example because it is shorter than the lengths in its headers, is skipped. Skipped frames are not counted.

Parsed datagrams go through the same channel matching as in `socket` mode.

### Fragments

The server does not reassemble IP fragments in `afpacket` mode:

- The first fragment of an IPv4 datagram passes the kernel filter. The parser counts it as the global `fragment` counter and drops it.
- Later IPv4 fragments are rejected by the kernel filter and are not counted.
- IPv6 fragments carry a fragment extension header, so the kernel filter rejects them and they are not counted.

A sender that emits datagrams larger than the path MTU therefore loses those datagrams in `afpacket` mode. The `fragment` counter shows one count per lost IPv4 datagram.

### Testing status

The filter is tested in a user-space BPF interpreter, without the outgoing-packet check that only the kernel can evaluate. The parser has unit tests. `TestAFPacketLive` in `internal/capture/afpacket_linux_test.go` captures real packets on `lo`, but it runs only as root or with `CAP_NET_RAW` and is skipped otherwise. CI runs the tests without root, so live AF_PACKET capture is not covered by CI.
