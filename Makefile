projectname := udp-tcp-tunnel
PKG := github.com/nerdneilsfield/udp-tcp-tunnel
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) -X $(PKG)/internal/buildinfo.Date=$(DATE)

.DEFAULT_GOAL := help
.PHONY: help build build-server build-client install run-server run-client test clean cover fmt fmt-check vet lint check release-check release-test hooks cross-build

help: ## list makefile targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

build: build-server build-client ## build server and client binaries

build-server: ## build the Linux-only server binary
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o udp-tcp-tunnel-server ./cmd/udp-tcp-tunnel-server

build-client: ## build the client binary
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o udp-tcp-tunnel-client ./cmd/udp-tcp-tunnel-client

cross-build: ## cross-compile every release target
	@set -e; for os in linux darwin windows; do for arch in amd64 arm64; do \
		echo "client $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -o /dev/null ./cmd/udp-tcp-tunnel-client; \
	done; done
	@set -e; for arch in amd64 arm64; do \
		echo "server linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -o /dev/null ./cmd/udp-tcp-tunnel-server; \
	done

install: ## install both binaries
	go install -ldflags '$(LDFLAGS)' ./cmd/udp-tcp-tunnel-server ./cmd/udp-tcp-tunnel-client

run-server: ## run the server (CONFIG=server.yaml)
	go run -ldflags '$(LDFLAGS)' ./cmd/udp-tcp-tunnel-server -c $(or $(CONFIG),examples/server.yaml)

run-client: ## run the client (CONFIG=client.yaml)
	go run -ldflags '$(LDFLAGS)' ./cmd/udp-tcp-tunnel-client -c $(or $(CONFIG),examples/client.yaml)

test: clean ## display test coverage
	go test --cover -parallel=1 -v -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | sort -rnk3

clean: ## clean up environment
	rm -rf coverage.out dist/ udp-tcp-tunnel-server udp-tcp-tunnel-client udp-tcp-tunnel-*.exe

cover: ## display test coverage
	go test -v -race ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

fmt: ## format go files
	go tool gofumpt -l -w .
	go tool goimports -w -local $(PKG) .
	gofmt -w .

fmt-check: ## check go formatting
	@test -z "$$(go tool gofumpt -l .)" || { echo "gofumpt: files need formatting"; exit 1; }
	@test -z "$$(go tool goimports -l -local $(PKG) .)" || { echo "goimports: files need formatting"; exit 1; }
	@test -z "$$(gofmt -l .)" || { echo "gofmt: files need formatting"; exit 1; }

hooks: ## install git hooks
	go tool lefthook install

vet: ## run go vet
	go vet ./...

lint: ## run golangci-lint
	golangci-lint run -c .golangci.yml

check: fmt-check lint vet test release-check ## run all checks

release-check: ## validate goreleaser config
	goreleaser check

release-test: ## test release
	goreleaser release --snapshot --clean
