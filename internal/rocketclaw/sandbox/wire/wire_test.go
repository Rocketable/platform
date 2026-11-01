package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	w := NewWriter(&buf)

	start := &InvokeStart{
		ExecId:   []byte("x-1"),
		Name:     []byte("upper"),
		Cwd:      []byte("/home/user/dir with space"),
		Args:     [][]byte{{}, []byte("a b"), []byte("line\nbreak"), []byte("ünïcødé"), {0xff, 0x00, 0xfe}},
		GuestEnv: [][]byte{[]byte("DEPLOY_TOKEN=evil")},
	}
	frames := []*Frame{
		{Body: &Frame_InvokeStart{InvokeStart: start}},
		{Body: &Frame_StdinChunk{StdinChunk: []byte{0, 1, 0xff, '\n', 0}}},
		{Body: &Frame_StdinEof{StdinEof: &StdinEOF{}}},
		{Body: &Frame_InvokeExit{InvokeExit: &InvokeExit{Code: 17}}},
	}

	for _, f := range frames {
		if err := w.Write(f); err != nil {
			t.Fatal(err)
		}
	}

	r := NewReader(&buf)
	for _, want := range frames {
		got, err := r.Read()
		if err != nil {
			t.Fatal(err)
		}

		if !proto.Equal(got, want) {
			t.Fatalf("Read = %v; want %v", got, want)
		}
	}

	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("Read at end = %v; want io.EOF", err)
	}
}

// F02: an advertised length over MaxFrame is refused from the varint alone, before allocating it.
func TestReadRejectsOversizedBeforeAllocating(t *testing.T) {
	for _, n := range []uint64{MaxFrame + 1, 1 << 31, 1 << 62} {
		in := binary.AppendUvarint(nil, n)

		var before, after runtime.MemStats

		runtime.ReadMemStats(&before)

		_, err := NewReader(bytes.NewReader(in)).Read()

		runtime.ReadMemStats(&after)

		if _, ok := errors.AsType[*protodelim.SizeTooLargeError](err); !ok {
			t.Fatalf("Read(len %d) = %v; want SizeTooLargeError", n, err)
		}

		// The Reader's own buffer is ChunkSize; anything near the advertised size would dwarf it.
		if got := after.TotalAlloc - before.TotalAlloc; got > 2*ChunkSize {
			t.Errorf("Read(len %d) allocated %d bytes; want the size check to fail first", n, got)
		}
	}
}

func delimited(b []byte) []byte {
	return append(binary.AppendUvarint(nil, uint64(len(b))), b...)
}

func marshal(t *testing.T, f *Frame) []byte {
	t.Helper()

	b, err := proto.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// G05: forged fields, such as an invented hostEnv, are rejected rather than ignored.
func TestReadRejectsInvalidFrames(t *testing.T) {
	start := &Frame{Version: Version, Body: &Frame_InvokeStart{InvokeStart: &InvokeStart{Name: []byte("deploy"), GuestEnv: [][]byte{[]byte("DEPLOY_TOKEN=fourth-fake")}}}}

	hostEnvInStart := func() []byte {
		inner, _ := proto.Marshal(start.GetInvokeStart())
		inner = protowire.AppendTag(inner, 6, protowire.BytesType)
		inner = protowire.AppendBytes(inner, []byte("DEPLOY_TOKEN=evil"))

		b := protowire.AppendTag(nil, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, Version)
		b = protowire.AppendTag(b, 2, protowire.BytesType)

		return protowire.AppendBytes(b, inner)
	}()

	hostEnvInFrame := protowire.AppendBytes(protowire.AppendTag(marshal(t, start), 99, protowire.BytesType), []byte("DEPLOY_TOKEN=evil"))

	tests := []struct {
		name string
		in   []byte
	}{
		{"unknown InvokeStart field", hostEnvInStart},
		{"unknown frame field", hostEnvInFrame},
		{"other version", marshal(t, &Frame{Version: 2, Body: &Frame_StdinEof{StdinEof: &StdinEOF{}}})},
		{"no version", marshal(t, &Frame{Body: &Frame_StdinEof{StdinEof: &StdinEOF{}}})},
		{"empty body", marshal(t, &Frame{Version: Version})},
		{"exit code out of range", marshal(t, &Frame{Version: Version, Body: &Frame_InvokeExit{InvokeExit: &InvokeExit{Code: 256}}})},
		{"garbage", []byte{0xff, 0xff, 0xff}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if f, err := NewReader(bytes.NewReader(delimited(tt.in))).Read(); err == nil {
				t.Fatalf("Read accepted %v", f)
			}
		})
	}

	if _, err := NewReader(bytes.NewReader(delimited(marshal(t, start)))).Read(); err != nil {
		t.Fatalf("Read of the unforged frame = %v", err)
	}
}

func TestWriteRefusesOversized(t *testing.T) {
	err := NewWriter(io.Discard).Write(&Frame{Body: &Frame_StdoutChunk{StdoutChunk: make([]byte, MaxFrame)}})
	if err == nil {
		t.Fatal("Write of an oversized frame succeeded")
	}
}

// F01: arbitrary bytes never panic, and every accepted frame re-encodes to an equal frame.
func FuzzRead(f *testing.F) {
	var seed bytes.Buffer

	_ = NewWriter(&seed).Write(&Frame{Body: &Frame_InvokeStart{InvokeStart: &InvokeStart{Name: []byte("n"), Args: [][]byte{[]byte("a")}}}})
	f.Add(seed.Bytes())
	f.Add(binary.AppendUvarint(nil, 1<<40))

	f.Fuzz(func(t *testing.T, in []byte) {
		got, err := NewReader(bytes.NewReader(in)).Read()
		if err != nil {
			return
		}

		var buf bytes.Buffer
		if err := NewWriter(&buf).Write(got); err != nil {
			t.Fatal(err)
		}

		again, err := NewReader(&buf).Read()
		if err != nil || !proto.Equal(again, got) {
			t.Fatalf("re-read %v, %v; want %v", again, err, got)
		}
	})
}
