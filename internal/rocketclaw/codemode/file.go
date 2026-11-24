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

type File struct {
	spec   FileSpec
	digest func() string
	config func() (image.Config, error)
}

func NewFile(spec FileSpec) *File {
	return &File{
		spec: spec,
		digest: sync.OnceValue(func() string {
			h := sha256.New()
			writeChunks(h, spec.Content)

			return hex.EncodeToString(h.Sum(nil))
		}),
		config: sync.OnceValues(func() (image.Config, error) {
			if !strings.HasPrefix(spec.Mime, "image/") {
				return image.Config{}, image.ErrFormat
			}

			cfg, _, err := image.DecodeConfig(strings.NewReader(spec.Content))
			if err != nil {
				return cfg, fmt.Errorf("decode image config: %w", err)
			}

			return cfg, nil
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
	cfg, err := f.config()

	return cfg.Width, cfg.Height, err == nil
}

func humanSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}

	value, unit := float64(n)/1024, "KB"
	for _, next := range []string{"MB", "GB"} {
		if value >= 1024 {
			value, unit = value/1024, next
		}
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
	if op != syntax.EQL && op != syntax.NEQ {
		return false, fmt.Errorf("file %s file not implemented", op)
	}

	other := y.(*File)

	return (f.spec.Mime == other.spec.Mime && f.digest() == other.digest()) == (op == syntax.EQL), nil
}

func (f *File) MarshalJSON() ([]byte, error) {
	var width, height *int
	if w, h, ok := f.Dimensions(); ok {
		width, height = &w, &h
	}

	summary := f.spec.Name + ", " + f.details()
	if f.spec.Source != "" {
		summary += ", from " + f.spec.Source
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
	}{f.spec.Name, height, f.spec.Mime, f.digest(), f.Size(), f.spec.Source, summary, width})
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

		return starlark.MakeInt(map[string]int{"width": w, "height": h}[name]), nil
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

	return nil, nil
}

func (f *File) AttrNames() []string {
	return ResultAttrNames("base64", "bytes", "height", "mime", "name", "sha256", "size", "source", "width")
}

func (f *File) details() string {
	size := humanSize(f.Size())
	if w, h, ok := f.Dimensions(); ok {
		return f.spec.Mime + ", " + fmt.Sprintf("%d × %d", w, h) + ", " + size
	}

	return f.spec.Mime + ", " + size
}

var (
	_ Result              = (*File)(nil)
	_ starlark.HasAttrs   = (*File)(nil)
	_ starlark.Comparable = (*File)(nil)
)
