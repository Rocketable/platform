package codemode

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
	"sync"

	_ "golang.org/x/image/webp"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type FileSpec struct {
	Name, Mime string
	Content    string
	Source     string
}

type FileMethods interface {
	Attr(f *File, name string) (starlark.Value, error)
	AttrNames() []string
}

type NoFileMethods struct{}

func (NoFileMethods) Attr(*File, string) (starlark.Value, error) { return nil, nil }
func (NoFileMethods) AttrNames() []string                        { return nil }

type imageConfig struct {
	width, height int
	ok            bool
}

type File struct {
	spec    FileSpec
	methods FileMethods
	digest  func() string
	config  func() imageConfig
}

func NewFile(spec FileSpec, methods FileMethods) *File {
	return &File{
		spec:    spec,
		methods: methods,
		digest: sync.OnceValue(func() string {
			h := sha256.New()
			writeChunks(h, spec.Content)

			return hex.EncodeToString(h.Sum(nil))
		}),
		config: sync.OnceValue(func() imageConfig {
			if !strings.HasPrefix(spec.Mime, "image/") {
				return imageConfig{}
			}

			cfg, _, err := image.DecodeConfig(strings.NewReader(spec.Content))
			if err != nil {
				return imageConfig{}
			}

			return imageConfig{width: cfg.Width, height: cfg.Height, ok: true}
		}),
	}
}

func writeChunks(w io.Writer, s string) {
	buf := make([]byte, min(len(s), 64<<10))
	for s != "" {
		n := copy(buf, s)
		_, _ = w.Write(buf[:n])
		s = s[n:]
	}
}

func (f *File) Name() string    { return f.spec.Name }
func (f *File) Mime() string    { return f.spec.Mime }
func (f *File) Content() string { return f.spec.Content }
func (f *File) Source() string  { return f.spec.Source }
func (f *File) Size() int       { return len(f.spec.Content) }
func (f *File) SHA256() string  { return f.digest() }

func (f *File) Dimensions() (width, height int, ok bool) {
	cfg := f.config()

	return cfg.width, cfg.height, cfg.ok
}

func (f *File) Summary() string {
	summary := f.spec.Name + ", " + f.details()
	if f.spec.Source != "" {
		summary += ", from " + f.spec.Source
	}

	return summary
}

func humanSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}

	value, unit := float64(n)/1024, "KB"
	for _, next := range []string{"MB", "GB"} {
		if value < 1024 {
			break
		}

		value, unit = value/1024, next
	}

	if value < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + " " + unit
	}

	return fmt.Sprintf("%.0f %s", value, unit)
}

func (f *File) String() string       { return fmt.Sprintf("%s (%s)", f.spec.Name, f.details()) }
func (f *File) Type() string         { return "file" }
func (f *File) Freeze()              {}
func (f *File) Truth() starlark.Bool { return true }
func (f *File) Ok() bool             { return true }

func (f *File) Hash() (uint32, error) {
	h, _ := starlark.String(f.digest()).Hash()

	return h, nil
}

func (f *File) CompareSameType(op syntax.Token, y starlark.Value, _ int) (bool, error) {
	other := y.(*File)
	same := f.spec.Mime == other.spec.Mime && f.digest() == other.digest()

	if op == syntax.EQL {
		return same, nil
	}

	if op == syntax.NEQ {
		return !same, nil
	}

	return false, fmt.Errorf("file %s file not implemented", op)
}

func (f *File) MarshalJSON() ([]byte, error) {
	var width, height *int
	if w, h, ok := f.Dimensions(); ok {
		width, height = &w, &h
	}

	encoded, err := json.Marshal(struct {
		File    string `json:"file"`
		Height  *int   `json:"height,omitempty"`
		Mime    string `json:"mime"`
		SHA256  string `json:"sha256"`
		Size    int    `json:"size"`
		Source  string `json:"source"`
		Summary string `json:"summary"`
		Width   *int   `json:"width,omitempty"`
	}{f.spec.Name, height, f.spec.Mime, f.digest(), f.Size(), f.spec.Source, f.Summary(), width})
	if err != nil {
		return nil, fmt.Errorf("encode file: %w", err)
	}

	return encoded, nil
}

func (f *File) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(f, nil, name); ok {
		return v, nil
	}

	switch name {
	case "name":
		return starlark.String(f.spec.Name), nil
	case "mime":
		return starlark.String(f.spec.Mime), nil
	case "size":
		return starlark.MakeInt(f.Size()), nil
	case "sha256":
		return starlark.String(f.digest()), nil
	case "source":
		return starlark.String(f.spec.Source), nil
	case "width", "height":
		w, h, ok := f.Dimensions()
		if !ok {
			return starlark.None, nil
		}

		if name == "width" {
			return starlark.MakeInt(w), nil
		}

		return starlark.MakeInt(h), nil
	case "bytes":
		return noArgsMethod("bytes", func() (starlark.Value, error) { return starlark.Bytes(f.spec.Content), nil }), nil
	case "base64":
		return noArgsMethod("base64", func() (starlark.Value, error) {
			var out strings.Builder

			out.Grow(base64.StdEncoding.EncodedLen(f.Size()))
			enc := base64.NewEncoder(base64.StdEncoding, &out)
			writeChunks(enc, f.spec.Content)
			_ = enc.Close()

			return starlark.String(out.String()), nil
		}), nil
	}

	method, err := f.methods.Attr(f, name)
	if err != nil {
		return nil, fmt.Errorf("file.%s: %w", name, err)
	}

	return method, nil
}

func (f *File) AttrNames() []string {
	return ResultAttrNames(append(f.methods.AttrNames(), "base64", "bytes", "height", "mime", "name", "sha256", "size", "source", "width")...)
}

func (f *File) details() string {
	parts := []string{f.spec.Mime}
	if w, h, ok := f.Dimensions(); ok {
		parts = append(parts, fmt.Sprintf("%d × %d", w, h))
	}

	return strings.Join(append(parts, humanSize(f.Size())), ", ")
}

var (
	_ Result              = (*File)(nil)
	_ starlark.HasAttrs   = (*File)(nil)
	_ starlark.Comparable = (*File)(nil)
)
