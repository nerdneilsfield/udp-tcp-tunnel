# udp-tcp-tunnel

A small multi-channel UDP over one TCP connection. Run `udp-tcp-tunnel-server -c examples/server.yaml` near the lidars and `udp-tcp-tunnel-client -c examples/client.yaml` on the workstation. Source IP and source port are not preserved; TCP head-of-line blocking applies.

`socket` mode owns the UDP port. Use Linux `afpacket` mode when another receiver owns it; it requires CAP_NET_RAW. Check traffic with `sudo tcpdump -ni eth0 -Q in 'udp and dst port 2368'`. TLS and a shared token are supported. The bounded server queue drops packets when full and disconnected traffic is dropped.

Build with `make build`; run `go test -race ./...`. GoReleaser produces separate server Linux and client Linux/macOS/Windows archives. No container images are published.

Multi-channel UDP over TCP tunnel with transparent packet forwarding.

Built from [go-template](https://github.com/nerdneilsfield/go-template): cobra, zap, golangci-lint v2, GoReleaser v2, GitHub Actions and a distroless Docker image.

## Layout

```
main.go                 signal handling, calls cmd.Execute
cmd/                    cobra commands
internal/buildinfo/     version, commit and date injected with -ldflags
internal/logging/       zap logger writing to stderr
```

## Commands

`make` and `just` provide the same targets:

| Target | Description |
| --- | --- |
| `build` | Build the binary with version info |
| `hooks` | Install the Git hooks |
| `test` | Run tests with coverage |
| `fmt` / `fmt-check` | Format with gofumpt and goimports (via `go tool`) |
| `lint` | Run golangci-lint |
| `check` | fmt-check, lint, vet, test and `goreleaser check` |
| `release-test` | Build a local snapshot release |

golangci-lint and goreleaser are installed separately. gofumpt, goimports and lefthook are `tool` dependencies in `go.mod`.

The Git hooks check staged Go files with gofumpt and goimports, and check `go.mod`/`go.sum` with `go mod tidy -diff` when either file is staged. The commit-msg hook accepts Conventional Commit messages and allows Git merge and revert messages.

## Release

Push a `v*` tag. The release workflow runs GoReleaser, publishes binaries and Docker images to GHCR and Docker Hub, and attests the binaries and images.

The workflow needs the repository secrets `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`. Without them the Docker Hub login step fails and nothing is released. To publish only to GHCR, remove the Docker Hub login step and the `nerdneils/udp-tcp-tunnel` image from `.goreleaser.yml`.
