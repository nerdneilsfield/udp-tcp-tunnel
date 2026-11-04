projectname := "udp-tcp-tunnel"
pkg := "github.com/nerdneilsfield/udp-tcp-tunnel"
version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit := `git rev-parse --short HEAD 2>/dev/null || echo none`
date := `date -u +%Y-%m-%dT%H:%M:%SZ`
ldflags := "-X {{pkg}}/internal/buildinfo.Version={{version}} -X {{pkg}}/internal/buildinfo.Commit={{commit}} -X {{pkg}}/internal/buildinfo.Date={{date}}"

# 列出所有可用的命令
default:
    @just --list

# 构建 server 和 client 二进制文件
build: build-server build-client

# 构建 server（仅 Linux）
build-server:
    CGO_ENABLED=0 go build -ldflags '{{ldflags}}' -o udp-tcp-tunnel-server ./cmd/udp-tcp-tunnel-server

# 构建 client
build-client:
    CGO_ENABLED=0 go build -ldflags '{{ldflags}}' -o udp-tcp-tunnel-client ./cmd/udp-tcp-tunnel-client

# 交叉编译所有发布目标
cross-build:
    #!/usr/bin/env bash
    set -euo pipefail
    for os in linux darwin windows; do
      for arch in amd64 arm64; do
        echo "client $os/$arch"
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o /dev/null ./cmd/udp-tcp-tunnel-client
      done
    done
    for arch in amd64 arm64; do
      echo "server linux/$arch"
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -o /dev/null ./cmd/udp-tcp-tunnel-server
    done

# 安装两个二进制文件
install:
    go install -ldflags '{{ldflags}}' ./cmd/udp-tcp-tunnel-server ./cmd/udp-tcp-tunnel-client

# 运行 server
run-server config="examples/server.yaml":
    go run -ldflags '{{ldflags}}' ./cmd/udp-tcp-tunnel-server -c {{config}}

# 运行 client
run-client config="examples/client.yaml":
    go run -ldflags '{{ldflags}}' ./cmd/udp-tcp-tunnel-client -c {{config}}

# 运行测试并显示覆盖率
test: clean
    go test --cover -parallel=1 -v -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | sort -rnk3

# 清理环境
clean:
    rm -rf coverage.out dist udp-tcp-tunnel-server udp-tcp-tunnel-client udp-tcp-tunnel-*.exe

# 显示测试覆盖率
cover:
    go test -v -race ./... -coverprofile=coverage.out
    go tool cover -func=coverage.out

# 格式化 Go 文件
fmt:
    go tool gofumpt -l -w .
    go tool goimports -w -local {{pkg}} .
    gofmt -w .

# 检查 Go 文件格式
fmt-check:
    test -z "$(go tool gofumpt -l .)" || { echo "gofumpt: files need formatting"; exit 1; }
    test -z "$(go tool goimports -l -local {{pkg}} .)" || { echo "goimports: files need formatting"; exit 1; }
    test -z "$(gofmt -l .)" || { echo "gofmt: files need formatting"; exit 1; }

# 安装 Git hooks
hooks:
    go tool lefthook install

# 运行 go vet
vet:
    go vet ./...

# 运行 linter
lint:
    golangci-lint run -c .golangci.yml

# 运行全部检查（fmt、lint、vet、test、release config）
check: fmt-check lint vet test release-check

# 校验 goreleaser 配置
release-check:
    goreleaser check

# 测试发布
release-test:
    goreleaser release --snapshot --clean
