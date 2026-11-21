package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

func gridColor(x, y int) color.NRGBA {
	return color.NRGBA{R: uint8(x * 100), G: uint8(y * 100), B: 50, A: 255}
}

func imageFixtures(t *testing.T) []*codemode.File {
	t.Helper()

	grid := image.NewNRGBA(image.Rect(0, 0, 2, 3))

	for y := range 3 {
		for x := range 2 {
			grid.SetNRGBA(x, y, gridColor(x, y))
		}
	}

	var gridPNG, photoJPEG, animatedGIF bytes.Buffer
	if err := png.Encode(&gridPNG, grid); err != nil {
		t.Fatal(err)
	}

	if err := jpeg.Encode(&photoJPEG, image.NewRGBA(image.Rect(0, 0, 8, 4)), nil); err != nil {
		t.Fatal(err)
	}

	frame := func(c uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 4, 2), palette.Plan9)
		for i := range p.Pix {
			p.Pix[i] = c
		}

		return p
	}
	if err := gif.EncodeAll(&animatedGIF, &gif.GIF{Image: []*image.Paletted{frame(1), frame(200)}, Delay: []int{0, 0}}); err != nil {
		t.Fatal(err)
	}

	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}

	gifHeader := func(width, height uint16) string {
		return "GIF89a" + string([]byte{byte(width), byte(width >> 8), byte(height), byte(height >> 8), 0, 0, 0})
	}

	file := func(name, mime, content string) *codemode.File {
		return newFileValue(codemode.FileSpec{Name: name, Mime: mime, Content: content, Source: "read " + name})
	}

	return []*codemode.File{
		file("grid.png", "image/png", gridPNG.String()),
		file("photo.jpg", "image/jpeg", photoJPEG.String()),
		file("anim.gif", "image/gif", animatedGIF.String()),
		file("pic.webp", "image/webp", string(webp)),
		file("doc.pdf", "application/pdf", "%PDF-1.4\n%%EOF\n"),
		file("broken.png", "image/png", "not a png"),
		file("truncated.png", "image/png", gridPNG.String()[:40]),
		file("huge.gif", "image/gif", gifHeader(4000, 4000)),
		file("limit.gif", "image/gif", gifHeader(3200, 3200)),
		file("over.gif", "image/gif", gifHeader(3200, 3201)),
	}
}

func runImageScript(t *testing.T, script string) (starlark.Value, error) {
	t.Helper()

	files := map[string]*codemode.File{}
	for _, f := range imageFixtures(t) {
		files[f.Name()] = f
	}

	value, err := codemode.Run(t.Context(), script, nil,
		func(context.Context, string, map[string]any) error { return nil },
		func(context.Context, string, string, map[string]any) (starlark.Value, error) {
			return nil, errors.New("no MCP tools")
		},
		[]codemode.HostTool{{
			Name: "read",
			CallValue: func(_ context.Context, args map[string]any) (starlark.Value, error) {
				path, _ := args["filePath"].(string)
				if f, ok := files[path]; ok {
					return f, nil
				}

				return nil, errors.New("File not found: " + path)
			},
		}})
	if err != nil {
		return nil, fmt.Errorf("run image script: %w", err)
	}

	return value, nil
}

func TestImageEditsReturnNewFiles(t *testing.T) {
	cases := []struct {
		name, expr               string
		wantName, wantMime       string
		wantSource               string
		wantWidth, wantHeight    int
		wantPixel                func(x, y int) color.NRGBA
		wantFirstFramePaletteIdx int
	}{
		{
			name: "crop clamps to the image", expr: `read(filePath="grid.png").crop(x=1, y=1, width=10, height=10)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → crop 1×2", wantWidth: 1, wantHeight: 2,
			wantPixel: func(x, y int) color.NRGBA { return gridColor(1+x, 1+y) },
		},
		{
			name: "crop with positional arguments", expr: `read(filePath="grid.png").crop(0, 2, 2, 1)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → crop 2×1", wantWidth: 2, wantHeight: 1,
			wantPixel: func(x, y int) color.NRGBA { return gridColor(x, 2+y) },
		},
		{
			name: "rotate 90 turns clockwise", expr: `read(filePath="grid.png").rotate(90)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → rotate 90", wantWidth: 3, wantHeight: 2,
			wantPixel: func(x, y int) color.NRGBA { return gridColor(y, 2-x) },
		},
		{
			name: "rotate 180", expr: `read(filePath="grid.png").rotate(degrees=180)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → rotate 180", wantWidth: 2, wantHeight: 3,
			wantPixel: func(x, y int) color.NRGBA { return gridColor(1-x, 2-y) },
		},
		{
			name: "rotate 270", expr: `read(filePath="grid.png").rotate(degrees=270)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → rotate 270", wantWidth: 3, wantHeight: 2,
			wantPixel: func(x, y int) color.NRGBA { return gridColor(1-y, x) },
		},
		{
			name: "resize width keeps the aspect ratio", expr: `read(filePath="grid.png").resize(width=4)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → resize 4×6", wantWidth: 4, wantHeight: 6,
		},
		{
			name: "resize height keeps the aspect ratio", expr: `read(filePath="grid.png").resize(height=9, width=None)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → resize 6×9", wantWidth: 6, wantHeight: 9,
		},
		{
			name: "resize both sides", expr: `read(filePath="grid.png").resize(width=5, height=1)`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → resize 5×1", wantWidth: 5, wantHeight: 1,
		},
		{
			name: "jpeg edits stay jpeg", expr: `read(filePath="photo.jpg").crop(x=0, y=0, width=4, height=4)`,
			wantName: "photo.jpg", wantMime: "image/jpeg", wantSource: "read photo.jpg → crop 4×4", wantWidth: 4, wantHeight: 4,
		},
		{
			name: "webp edits become png", expr: `read(filePath="pic.webp").resize(width=3)`,
			wantName: "pic.png", wantMime: "image/png", wantSource: "read pic.webp → resize 3×3", wantWidth: 3, wantHeight: 3,
		},
		{
			name: "gif edits keep the first frame", expr: `read(filePath="anim.gif").rotate(degrees=90)`,
			wantName: "anim.gif", wantMime: "image/gif", wantSource: "read anim.gif → rotate 90", wantWidth: 2, wantHeight: 4,
			wantFirstFramePaletteIdx: 1,
		},
		{
			name: "convert to jpeg", expr: `read(filePath="grid.png").convert("jpeg", quality=50)`,
			wantName: "grid.jpg", wantMime: "image/jpeg", wantSource: "read grid.png → convert jpeg", wantWidth: 2, wantHeight: 3,
		},
		{
			name: "convert to gif", expr: `read(filePath="grid.png").convert(format="gif")`,
			wantName: "grid.gif", wantMime: "image/gif", wantSource: "read grid.png → convert gif", wantWidth: 2, wantHeight: 3,
		},
		{
			name: "convert round trip", expr: `read(filePath="grid.png").convert(format="jpeg").convert(format="gif").convert(format="png")`,
			wantName: "grid.png", wantMime: "image/png", wantSource: "read grid.png → convert jpeg → convert gif → convert png", wantWidth: 2, wantHeight: 3,
		},
		{
			name: "chain", expr: `read(filePath="grid.png").crop(x=0, y=0, width=2, height=2).resize(width=4).rotate(degrees=90).convert(format="jpeg")`,
			wantName: "grid.jpg", wantMime: "image/jpeg", wantSource: "read grid.png → crop 2×2 → resize 4×4 → rotate 90 → convert jpeg", wantWidth: 4, wantHeight: 4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := runImageScript(t, "def main():\n    return "+tc.expr+"\n")
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			f, ok := value.(*codemode.File)
			if !ok {
				t.Fatalf("result = %s %v, want file", value.Type(), value)
			}

			if f.Name() != tc.wantName || f.Mime() != tc.wantMime || f.Source() != tc.wantSource {
				t.Fatalf("file = %q %q %q, want %q %q %q", f.Name(), f.Mime(), f.Source(), tc.wantName, tc.wantMime, tc.wantSource)
			}

			img, format, errDecode := image.Decode(strings.NewReader(f.Content()))
			if errDecode != nil {
				t.Fatalf("decode edited file: %v", errDecode)
			}

			if "image/"+format != tc.wantMime {
				t.Fatalf("encoded format = %s, want %s", format, tc.wantMime)
			}

			if b := img.Bounds(); b.Dx() != tc.wantWidth || b.Dy() != tc.wantHeight {
				t.Fatalf("decoded size = %d × %d, want %d × %d", b.Dx(), b.Dy(), tc.wantWidth, tc.wantHeight)
			}

			if w, h, ok := f.Dimensions(); !ok || w != tc.wantWidth || h != tc.wantHeight {
				t.Fatalf("Dimensions() = %d, %d, %v, want %d, %d", w, h, ok, tc.wantWidth, tc.wantHeight)
			}

			if tc.wantPixel != nil {
				for y := range tc.wantHeight {
					for x := range tc.wantWidth {
						if got := color.NRGBAModel.Convert(img.At(x, y)); got != tc.wantPixel(x, y) {
							t.Fatalf("pixel (%d, %d) = %v, want %v", x, y, got, tc.wantPixel(x, y))
						}
					}
				}
			}

			if tc.wantFirstFramePaletteIdx != 0 {
				want := palette.Plan9[tc.wantFirstFramePaletteIdx]
				if got, wantRGBA := color.RGBAModel.Convert(img.At(0, 0)), color.RGBAModel.Convert(want); got != wantRGBA {
					t.Fatalf("pixel (0, 0) = %v, want first frame color %v", got, wantRGBA)
				}
			}
		})
	}
}

func TestImageEditFailuresAreResults(t *testing.T) {
	cases := []struct {
		name, expr, wantTool, wantMessage string
	}{
		{
			name: "failed read passes through every method", expr: `read(filePath="missing.png").crop(x=0, y=0, width=1, height=1).resize(width=2).rotate(degrees=90).convert(format="gif")`,
			wantTool: "read", wantMessage: "File not found: missing.png",
		},
		{
			name: "pdf", expr: `read(filePath="doc.pdf").crop(x=0, y=0, width=1, height=1).resize(width=2)`,
			wantTool: "crop", wantMessage: "application/pdf is not an image",
		},
		{
			name: "decode cap from the header", expr: `read(filePath="huge.gif").rotate(degrees=90)`,
			wantTool: "rotate", wantMessage: "image too large to edit (4000 × 4000 pixels; limit 10,240,000)",
		},
		{
			name: "one pixel over the cap", expr: `read(filePath="over.gif").convert(format="png")`,
			wantTool: "convert", wantMessage: "image too large to edit (3200 × 3201 pixels; limit 10,240,000)",
		},
		{
			name: "exactly the cap is decoded", expr: `read(filePath="limit.gif").convert(format="png")`,
			wantTool: "convert", wantMessage: "cannot decode image/gif image: gif: reading frames: unexpected EOF",
		},
		{
			name: "resize target over the cap", expr: `read(filePath="grid.png").resize(width=100000)`,
			wantTool: "resize", wantMessage: "image too large to edit (100000 × 150000 pixels; limit 10,240,000)",
		},
		{
			name: "undecodable header", expr: `read(filePath="broken.png").resize(width=1)`,
			wantTool: "resize", wantMessage: "cannot decode image/png image",
		},
		{
			name: "truncated pixels", expr: `read(filePath="truncated.png").resize(width=1)`,
			wantTool: "resize", wantMessage: "cannot decode image/png image: unexpected EOF",
		},
		{
			name: "rotate by an unsupported angle stops the chain", expr: `read(filePath="grid.png").rotate(degrees=45).convert(format="png")`,
			wantTool: "rotate", wantMessage: "degrees must be 90, 180 or 270, got 45",
		},
		{
			name: "webp encoding", expr: `read(filePath="grid.png").convert(format="webp")`,
			wantTool: "convert", wantMessage: `format must be "png", "jpeg" or "gif", got "webp"`,
		},
		{
			name: "jpeg quality", expr: `read(filePath="grid.png").convert(format="jpeg", quality=0)`,
			wantTool: "convert", wantMessage: "quality must be between 1 and 100, got 0",
		},
		{
			name: "resize without sides", expr: `read(filePath="grid.png").resize()`,
			wantTool: "resize", wantMessage: "give width, height, or both",
		},
		{
			name: "resize to zero", expr: `read(filePath="grid.png").resize(width=0)`,
			wantTool: "resize", wantMessage: "width and height must be positive",
		},
		{
			name: "crop to zero", expr: `read(filePath="grid.png").crop(x=0, y=0, width=0, height=1)`,
			wantTool: "crop", wantMessage: "width and height must be positive",
		},
		{
			name: "crop outside the image", expr: `read(filePath="grid.png").crop(x=5, y=0, width=1, height=1)`,
			wantTool: "crop", wantMessage: "rectangle x=5 y=0 1 × 1 is outside the image (2 × 3)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := runImageScript(t, "def main():\n    return "+tc.expr+"\n")
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			failure, ok := value.(*codemode.Error)
			if !ok {
				t.Fatalf("result = %s %v, want error", value.Type(), value)
			}

			if failure.Tool() != tc.wantTool || failure.Message() != tc.wantMessage {
				t.Fatalf("error = %q %q, want %q %q", failure.Tool(), failure.Message(), tc.wantTool, tc.wantMessage)
			}
		})
	}
}

func TestImageEditUnwrapStopsTheScript(t *testing.T) {
	_, err := runImageScript(t, "def main():\n    return read(filePath=\"doc.pdf\").crop(x=0, y=0, width=1, height=1).unwrap()\n")
	if err == nil || !strings.HasSuffix(err.Error(), "crop: application/pdf is not an image") {
		t.Fatalf("Run() error = %v, want crop: application/pdf is not an image", err)
	}
}

func TestImageEditBadArgumentTypesRaise(t *testing.T) {
	_, err := runImageScript(t, "def main():\n    return read(filePath=\"grid.png\").crop(x=\"a\", y=0, width=1, height=1)\n")
	if err == nil || !strings.Contains(err.Error(), `crop arguments: crop: for parameter "x": got string, want int`) {
		t.Fatalf("Run() error = %v, want crop argument error", err)
	}
}

func TestImageOverTheCapCanStillBeReturned(t *testing.T) {
	value, err := runImageScript(t, `def main():
    img = read(filePath="huge.gif")
    return (img.width, img.height, img.rotate(degrees=90).ok, img, sorted([n for n in dir(img) if n in ["convert", "crop", "resize", "rotate"]]))
`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := value.(starlark.Tuple)
	if got[0] != starlark.MakeInt(4000) || got[1] != starlark.MakeInt(4000) || got[2] != starlark.False || got[4].String() != `["convert", "crop", "resize", "rotate"]` {
		t.Fatalf("result = %v", got)
	}

	if f, ok := got[3].(*codemode.File); !ok || f.Name() != "huge.gif" || f.Source() != "read huge.gif" {
		t.Fatalf("returned file = %v, want the original huge.gif", got[3])
	}
}
