// Package cli builds the cobra command shared by the server and client
// binaries: a root command that runs the service, plus a version command.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/buildinfo"
	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/logging"
)

// Spec describes one binary.
type Spec struct {
	// Name is the binary name, e.g. "udp-tcp-tunnel-server".
	Name string
	// Short is the one-line command description.
	Short string
	// DefaultConfig is the default value of --config.
	DefaultConfig string
	// Run executes the service until ctx is canceled.
	Run func(ctx context.Context, log *zap.Logger, configPath string) error
}

// NewRootCmd builds the command tree for a binary.
func NewRootCmd(spec Spec) *cobra.Command {
	var (
		verbose    bool
		configPath string
	)
	cmd := &cobra.Command{
		Use:           spec.Name,
		Short:         spec.Short,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := logging.New(verbose)
			defer func() { _ = log.Sync() }()
			return spec.Run(cmd.Context(), log, configPath)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", spec.DefaultConfig, "path to the YAML config file")
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	cmd.AddCommand(newVersionCmd(spec.Name))
	return cmd
}

func newVersionCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"%s %s\ncommit: %s\nbuilt: %s\ngo: %s %s/%s\n",
				name, buildinfo.Version, buildinfo.Commit, buildinfo.Date,
				runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}

// Main runs the binary and returns the process exit code. SIGINT and
// SIGTERM cancel the context for a graceful shutdown.
func Main(spec Spec) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := NewRootCmd(spec).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
