package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestVersionCmd(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCmd(Spec{Name: "udp-tcp-tunnel-test", DefaultConfig: "x.yaml"})
	root.SetOut(&out)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.HasPrefix(out.String(), "udp-tcp-tunnel-test dev\n") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestConfigFlag(t *testing.T) {
	var got string
	spec := Spec{
		Name:          "udp-tcp-tunnel-test",
		DefaultConfig: "default.yaml",
		Run: func(_ context.Context, _ *zap.Logger, path string) error {
			got = path
			return nil
		},
	}
	for args, want := range map[string]string{"": "default.yaml", "-c custom.yaml": "custom.yaml", "--config long.yaml": "long.yaml"} {
		root := NewRootCmd(spec)
		root.SetArgs(strings.Fields(args))
		if err := root.Execute(); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
		if got != want {
			t.Fatalf("%q: config %q, want %q", args, got, want)
		}
	}
}
