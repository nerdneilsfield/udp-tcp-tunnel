package tunnel

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		channel uint16
		payload []byte
	}{
		{"small", 3, []byte("abc")},
		{"empty", 7, nil},
		{"max channel", 0xffff, bytes.Repeat([]byte{0x5a}, 1206)},
		{"max payload", 1, bytes.Repeat([]byte{0xff}, MaxPayload)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tc.channel, tc.payload); err != nil {
				t.Fatal(err)
			}
			if want := FrameHeaderLen + len(tc.payload); buf.Len() != want {
				t.Fatalf("encoded %d bytes, want %d", buf.Len(), want)
			}
			ch, payload, err := ReadFrame(&buf)
			if err != nil || ch != tc.channel || !bytes.Equal(payload, tc.payload) {
				t.Fatalf("got (%d, %d bytes, %v)", ch, len(payload), err)
			}
		})
	}
}

func TestFrameWireFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, 0x0102, []byte{0xaa, 0xbb}); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x02, 0x00, 0x00, 0x00, 0x02, 0xaa, 0xbb}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got % x, want % x", buf.Bytes(), want)
	}
}

func TestFrameStream(t *testing.T) {
	var buf bytes.Buffer
	for i := range 3 {
		if err := WriteFrame(&buf, uint16(i+1), bytes.Repeat([]byte{byte(i)}, i*10)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		ch, p, err := ReadFrame(&buf)
		if err != nil || ch != uint16(i+1) || len(p) != i*10 {
			t.Fatalf("frame %d: (%d, %d bytes, %v)", i, ch, len(p), err)
		}
	}
	if _, _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestBadFrames(t *testing.T) {
	if err := WriteFrame(io.Discard, 1, make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("oversized write: %v", err)
	}

	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"oversized length", []byte{0, 1, 0xff, 0xff, 0xff, 0xff}, ErrPayloadTooLarge},
		{"just over max", []byte{0, 1, 0, 1, 0, 0}, ErrPayloadTooLarge},
		{"empty stream", nil, io.EOF},
		{"truncated header", []byte{0, 1, 0}, io.ErrUnexpectedEOF},
		{"truncated payload", []byte{0, 1, 0, 0, 0, 5, 'a', 'b'}, io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ReadFrame(bytes.NewReader(tc.data)); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHelloRoundTrip(t *testing.T) {
	in := Hello{Token: "s3cret", Channels: []uint16{1, 2, 300}}
	var buf bytes.Buffer
	if err := WriteHello(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadHello(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.Token != in.Token || len(out.Channels) != 3 || out.Channels[2] != 300 {
		t.Fatalf("got %+v", out)
	}
}

func TestHelloEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHello(&buf, Hello{}); err != nil {
		t.Fatal(err)
	}
	out, err := ReadHello(&buf)
	if err != nil || out.Token != "" || len(out.Channels) != 0 {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestBadHello(t *testing.T) {
	if err := WriteHello(io.Discard, Hello{Token: strings.Repeat("x", 256)}); !errors.Is(err, ErrTokenTooLong) {
		t.Fatalf("long token: %v", err)
	}
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"bad magic", []byte("XXXX\x01\x00\x00\x00"), ErrBadMagic},
		{"bad version", []byte("UTT1\x09\x00\x00\x00"), ErrBadVersion},
		{"truncated token", []byte("UTT1\x01\x05ab"), io.ErrUnexpectedEOF},
		{"truncated channels", []byte("UTT1\x01\x00\x00\x02\x00\x01"), io.ErrUnexpectedEOF},
		{"empty", nil, io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadHello(bytes.NewReader(tc.data)); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHelloCheck(t *testing.T) {
	known := map[uint16]struct{}{1: {}, 2: {}}
	tests := []struct {
		name  string
		hello Hello
		want  Status
	}{
		{"ok", Hello{Token: "t", Channels: []uint16{1, 2}}, StatusOK},
		{"subset", Hello{Token: "t", Channels: []uint16{2}}, StatusOK},
		{"bad token", Hello{Token: "x", Channels: []uint16{1}}, StatusAuthFailed},
		{"unknown channel", Hello{Token: "t", Channels: []uint16{1, 9}}, StatusUnknownChannel},
	}
	for _, tc := range tests {
		if got := tc.hello.Check("t", known); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestReply(t *testing.T) {
	want := map[Status]error{
		StatusOK:             nil,
		StatusBadVersion:     ErrBadVersion,
		StatusAuthFailed:     ErrAuthFailed,
		StatusUnknownChannel: ErrUnknownChannel,
	}
	for status, wantErr := range want {
		var buf bytes.Buffer
		if err := WriteReply(&buf, status); err != nil {
			t.Fatal(err)
		}
		if err := ReadReply(&buf); !errors.Is(err, wantErr) {
			t.Errorf("status %d: got %v, want %v", status, err, wantErr)
		}
	}
	if err := ReadReply(strings.NewReader("NOPE\x01\x00")); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("bad magic reply: %v", err)
	}
	if err := ReadReply(strings.NewReader("UTT1\x01\x7f")); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestHelloCompressionWireFormat(t *testing.T) {
	// Without compression the hello is the original version 1 bytes.
	var b bytes.Buffer
	must(t, WriteHello(&b, Hello{Channels: []uint16{1}}))
	if got, want := b.Bytes(), []byte{0x55, 0x54, 0x54, 0x31, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01}; !bytes.Equal(got, want) {
		t.Fatalf("v1 hello = % x, want % x", got, want)
	}

	// With compression it is version 2 plus one algorithm byte.
	b.Reset()
	must(t, WriteHello(&b, Hello{Token: "t", Channels: []uint16{1}, Compression: AlgoZstd}))
	if got, want := b.Bytes(), []byte{0x55, 0x54, 0x54, 0x31, 0x02, 0x01, 't', 0x00, 0x01, 0x00, 0x01, 0x01}; !bytes.Equal(got, want) {
		t.Fatalf("v2 hello = % x, want % x", got, want)
	}
	h, err := ReadHello(&b)
	must(t, err)
	if h.Version != VersionCompression || h.Compression != AlgoZstd || h.Token != "t" || len(h.Channels) != 1 {
		t.Fatalf("parsed %+v", h)
	}
}

func TestReplyAlgo(t *testing.T) {
	var b bytes.Buffer
	must(t, WriteReplyAlgo(&b, StatusOK, AlgoS2))
	if got, want := b.Bytes(), []byte{0x55, 0x54, 0x54, 0x31, 0x02, 0x00, 0x02}; !bytes.Equal(got, want) {
		t.Fatalf("v2 reply = % x, want % x", got, want)
	}
	algo, v2, err := ReadReplyAlgo(&b)
	if err != nil || !v2 || algo != AlgoS2 {
		t.Fatalf("got %v %v %v", algo, v2, err)
	}

	b.Reset()
	must(t, WriteReplyAlgo(&b, StatusCompressionMismatch, AlgoNone))
	if _, v2, err = ReadReplyAlgo(&b); !errors.Is(err, ErrCompressionMismatch) || !v2 {
		t.Fatalf("mismatch reply: %v %v", v2, err)
	}

	// A version 1 reply is exactly the original six bytes and carries no algorithm.
	b.Reset()
	must(t, WriteReply(&b, StatusOK))
	if b.Len() != 6 {
		t.Fatalf("v1 reply is %d bytes", b.Len())
	}
	if _, v2, err = ReadReplyAlgo(&b); err != nil || v2 {
		t.Fatalf("v1 reply: %v %v", v2, err)
	}
}

func TestCompressedFrameFlag(t *testing.T) {
	buf, err := appendFrame(nil, 3, true, []byte("abc"))
	must(t, err)
	if want := []byte{0, 3, 0x80, 0, 0, 3, 'a', 'b', 'c'}; !bytes.Equal(buf, want) {
		t.Fatalf("frame = % x, want % x", buf, want)
	}
	ch, compressed, p, err := readFrame(bytes.NewReader(buf))
	if err != nil || ch != 3 || !compressed || string(p) != "abc" {
		t.Fatalf("readFrame: %d %v %q %v", ch, compressed, p, err)
	}
	// The plain reader refuses compressed frames; raw frames are unchanged.
	if _, _, err = ReadFrame(bytes.NewReader(buf)); err == nil {
		t.Fatal("ReadFrame accepted a compressed frame")
	}
	raw, err := AppendFrame(nil, 3, []byte("abc"))
	must(t, err)
	if want := []byte{0, 3, 0, 0, 0, 3, 'a', 'b', 'c'}; !bytes.Equal(raw, want) {
		t.Fatalf("raw frame = % x", raw)
	}
}
