// Command udp-tcp-tunnel-client receives datagrams from a
// udp-tcp-tunnel-server and re-emits them as local UDP packets.
package main

import (
	"context"
	"os"

	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/cli"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
)

func main() {
	os.Exit(cli.Main(cli.Spec{
		Name:          "udp-tcp-tunnel-client",
		Short:         "Receive datagrams from the TCP tunnel and re-emit them as local UDP.",
		DefaultConfig: "client.yaml",
		Run:           run,
	}))
}

func run(ctx context.Context, log *zap.Logger, path string) error {
	cfg, err := config.LoadClient(path)
	if err != nil {
		return err
	}
	client, err := tunnel.NewClient(cfg, log)
	if err != nil {
		return err
	}
	return client.Run(ctx)
}
