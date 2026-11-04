//go:build server

package main

import (
	"context"
	"fmt"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/logging"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/tunnel"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var f string
	c := &cobra.Command{Use: "udp-tcp-tunnel-server", RunE: func(*cobra.Command, []string) error {
		var x config.Server
		if e := config.Load(f, &x); e != nil {
			return e
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return tunnel.NewServer(x, logging.New(false)).Run(ctx)
	}}
	c.Flags().StringVarP(&f, "config", "c", "server.yaml", "config file")
	if e := c.Execute(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
