// Package wire is the emulated shell's relay protocol: length-delimited protobuf Frames over a Unix socket, one
// invocation per connection (wire.proto, docs/explainers/2026-11-01-emulated-shell-sandbox.md).
package wire

//go:generate protoc --go_out=. --go_opt=paths=source_relative wire.proto

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sync"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
)

// Version is the only protocol version the broker accepts.
const Version = 1

// MaxFrame is the largest encoded frame, 1 MiB (spec §10.1); larger lengths fail before allocation.
const MaxFrame = 1 << 20

// ChunkSize is the largest stdin, stdout or stderr chunk a peer sends.
const ChunkSize = 64 << 10

// Reader reads frames from one connection.
type Reader struct {
	r *bufio.Reader
}

// NewReader returns a Reader on r.
func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReaderSize(r, ChunkSize)} }

// Read reads one frame. It refuses frames over MaxFrame, other versions, empty bodies, and unknown
// fields anywhere, so a forged field such as a host environment is an error, not silently ignored.
func (r *Reader) Read() (*Frame, error) {
	f := &Frame{}
	if err := (protodelim.UnmarshalOptions{MaxSize: MaxFrame}).UnmarshalFrom(r.r, f); err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}

	if f.GetVersion() != Version {
		return nil, fmt.Errorf("wire: unsupported version %d", f.GetVersion())
	}

	if f.GetBody() == nil {
		return nil, errors.New("wire: empty frame")
	}

	if len(f.ProtoReflect().GetUnknown()) != 0 {
		return nil, errors.New("wire: unknown frame field")
	}

	if start := f.GetInvokeStart(); start != nil && len(start.ProtoReflect().GetUnknown()) != 0 {
		return nil, errors.New("wire: unknown InvokeStart field")
	}

	if exit := f.GetInvokeExit(); exit != nil && exit.GetCode() > 255 {
		return nil, fmt.Errorf("wire: exit code %d out of range", exit.GetCode())
	}

	return f, nil
}

// Writer writes frames; it is safe for concurrent use, so stdout and stderr can share a connection.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write sets the version and writes f as one length-delimited message.
func (w *Writer) Write(f *Frame) error {
	f.Version = Version

	if proto.Size(f) > MaxFrame {
		return errors.New("wire: frame exceeds the frame limit")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := protodelim.MarshalTo(w.w, f); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	return nil
}
