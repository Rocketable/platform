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

const Version = 1

const MaxFrame = 1 << 20

const ChunkSize = 64 << 10

type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReaderSize(r, ChunkSize)} }

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

	if code := f.GetInvokeExit().GetCode(); code > 255 {
		return nil, fmt.Errorf("wire: exit code %d out of range", code)
	}

	return f, nil
}

type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

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
