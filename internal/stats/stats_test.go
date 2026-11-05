package stats

import (
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestSnapshotRates(t *testing.T) {
	reg := New("server", zap.NewNop())
	ch := reg.AddChannel(1, "a")
	reg.AddChannel(2, "b")
	start := reg.last

	for range 200 {
		ch.Add(1250) // 200 packets, 250000 bytes
	}
	ch.Drop(QueueFull)
	ch.Drop(QueueFull)
	ch.Drop(Oversized)
	reg.Global().Drop(UnknownChannel)
	reg.Global().SetDrops(CaptureDrop, 7)

	channels, globals := reg.Snapshot(start.Add(2 * time.Second))
	if len(channels) != 2 {
		t.Fatalf("got %d channels", len(channels))
	}
	a := channels[0]
	if a.ID != 1 || a.Name != "a" || a.Packets != 200 || a.Bytes != 250000 {
		t.Fatalf("unexpected totals: %+v", a)
	}
	if a.PPS != 100 || a.Mbps != 1 {
		t.Fatalf("pps=%v mbps=%v, want 100 and 1", a.PPS, a.Mbps)
	}
	if a.DropCount(QueueFull) != 2 || a.DropCount(Oversized) != 1 || a.DropCount(NoClient) != 0 {
		t.Fatalf("unexpected drops: %+v", a.Drops)
	}
	if len(globals) != 1 {
		t.Fatalf("got %d globals", len(globals))
	}
	global := globals[0]
	if global.DropCount(UnknownChannel) != 1 || global.DropCount(CaptureDrop) != 7 {
		t.Fatalf("unexpected global drops: %+v", global.Drops)
	}

	// Rates are per interval: nothing new means zero.
	channels, _ = reg.Snapshot(start.Add(4 * time.Second))
	if channels[0].PPS != 0 || channels[0].Packets != 200 {
		t.Fatalf("second interval: %+v", channels[0])
	}
}

func TestLogFields(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	reg := New("client", zap.New(core))
	ch := reg.AddChannel(3, "lidar")
	ch.Add(100)
	ch.Drop(WriteError)

	reg.Log(reg.last.Add(time.Second))

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("got %d log entries, want channel + global", len(entries))
	}
	f := entries[0].ContextMap()
	if fmt.Sprint(f["channel"], f["packets"], f["bytes"], f["drop_write_error"]) != "3 1 100 1" {
		t.Fatalf("unexpected fields: %v", f)
	}
	fields := entries[0].ContextMap()
	for _, key := range []string{
		"pps", "mbps", "drop_queue_full", "drop_unknown_channel", "drop_oversized",
		"drop_fragment", "drop_capture_drop", "drop_capture_freeze", "drop_no_client",
	} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing field %s", key)
		}
	}
}

func TestUpstreamLogFields(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	reg := New("client", zap.New(core))
	ch := reg.AddUpstreamChannel("east", 1, "lidar")
	ch.Add(10)
	reg.UpstreamGlobal("east").Drop(UnknownChannel)

	reg.Log(reg.last.Add(time.Second))

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("got %d log entries", len(entries))
	}
	if entries[0].ContextMap()["upstream"] != "east" {
		t.Fatalf("channel missing upstream: %v", entries[0].ContextMap())
	}
	if entries[1].ContextMap()["upstream"] != "east" {
		t.Fatalf("global missing upstream: %v", entries[1].ContextMap())
	}
}

func TestReasonNames(t *testing.T) {
	want := map[Reason]string{
		QueueFull: "queue_full", UnknownChannel: "unknown_channel", Oversized: "oversized",
		Fragment: "fragment", CaptureDrop: "capture_drop", CaptureFreeze: "capture_freeze",
		NoClient: "no_client", WriteError: "write_error",
	}
	for r, name := range want {
		if r.String() != name {
			t.Errorf("Reason(%d) = %q, want %q", r, r, name)
		}
	}
}
