// Command udp-tcp-tunnel-server captures UDP datagrams and forwards them to
// a udp-tcp-tunnel-client over one TCP connection.
package main

import (
	"context"
	"os"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/cli"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/server"
)

func main() {
	os.Exit(cli.Main(cli.Spec{
		Name:          "udp-tcp-tunnel-server",
		Short:         "Capture UDP datagrams and forward them over a single TCP tunnel.",
		DefaultConfig: "server.yaml",
		Run:           run,
	}))
}

func run(ctx context.Context, log *zap.Logger, path string) error {
	cfg, err := config.LoadServer(path)
	if err != nil {
		return err
	}
	return server.New(cfg, log).Run(ctx)
}
