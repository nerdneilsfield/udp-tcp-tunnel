package config_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nerdneilsfield/udp-tcp-tunnel/internal/config"
)

func TestClientFullExampleLoads(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	// this file will be copied next to config tests - instead use relative from module
	_ = file
	path := filepath.Join("..", "..", "examples", "client-full.yaml")
	c, err := config.LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Upstreams) != 5 {
		t.Fatalf("upstreams=%d, want 5", len(c.Upstreams))
	}
}
