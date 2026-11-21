package harness

import (
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"path"
	"strings"

	"go.starlark.net/starlark"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

const (
	maxEditPixels      = 10_240_000
	defaultJPEGQuality = 90
)

type imageOps struct{}

func (imageOps) AttrNames() []string { return []string{"convert", "crop", "resize", "rotate"} }

func (imageOps) Attr(f *codemode.File, name string) (starlark.Value, error) {
	var edit func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)

	switch name {
	case "crop":
		edit = func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var x, y, width, height int32
			if err := starlark.UnpackArgs(name, args, kwargs, "x", &x, "y", &y, "width", &width, "height", &height); err != nil {
				return nil, fmt.Errorf("%s arguments: %w", name, err)
			}

			if width <= 0 || height <= 0 {
				return codemode.NewError(name, "width and height must be positive"), nil
			}

			return editImage(f, name, "", defaultJPEGQuality, func(img image.Image) (image.Image, string, string) {
				b := img.Bounds()

				r := image.Rect(int(x), int(y), int(x)+int(width), int(y)+int(height)).Add(b.Min).Intersect(b)
				if r.Empty() {
					return nil, "", fmt.Sprintf("rectangle x=%d y=%d %d × %d is outside the image (%d × %d)", x, y, width, height, b.Dx(), b.Dy())
				}

				return img.(interface {
					SubImage(image.Rectangle) image.Image
				}).SubImage(r), fmt.Sprintf("crop %d×%d", r.Dx(), r.Dy()), ""
			}), nil
		}
	case "resize":
		edit = func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			width, height := starlark.Value(starlark.None), starlark.Value(starlark.None)
			if err := starlark.UnpackArgs(name, args, kwargs, "width?", &width, "height?", &height); err != nil {
				return nil, fmt.Errorf("%s arguments: %w", name, err)
			}

			if width == starlark.None && height == starlark.None {
				return codemode.NewError(name, "give width, height, or both"), nil
			}

			w, errWidth := optionalImageSide(width, "width")
			if errWidth != nil {
				return nil, errWidth
			}

			h, errHeight := optionalImageSide(height, "height")
			if errHeight != nil {
				return nil, errHeight
			}

			if (width != starlark.None && w <= 0) || (height != starlark.None && h <= 0) {
				return codemode.NewError(name, "width and height must be positive"), nil
			}

			return editImage(f, name, "", defaultJPEGQuality, func(img image.Image) (image.Image, string, string) {
				b := img.Bounds()
				if w == 0 {
					w = max(1, (b.Dx()*h+b.Dy()/2)/b.Dy())
				}

				if h == 0 {
					h = max(1, (b.Dy()*w+b.Dx()/2)/b.Dx())
				}

				if w*h > maxEditPixels {
					return nil, "", imageTooLarge(w, h)
				}

				dst := image.NewRGBA(image.Rect(0, 0, w, h))
				xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)

				return dst, fmt.Sprintf("resize %d×%d", w, h), ""
			}), nil
		}
	case "rotate":
		edit = func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var degrees int
			if err := starlark.UnpackArgs(name, args, kwargs, "degrees", &degrees); err != nil {
				return nil, fmt.Errorf("%s arguments: %w", name, err)
			}

			if degrees != 90 && degrees != 180 && degrees != 270 {
				return codemode.NewError(name, fmt.Sprintf("degrees must be 90, 180 or 270, got %d", degrees)), nil
			}

			return editImage(f, name, "", defaultJPEGQuality, func(img image.Image) (image.Image, string, string) {
				return rotateImage(img, degrees), fmt.Sprintf("rotate %d", degrees), ""
			}), nil
		}
	case "convert":
		edit = func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var format string

			quality := defaultJPEGQuality
			if err := starlark.UnpackArgs(name, args, kwargs, "format", &format, "quality?", &quality); err != nil {
				return nil, fmt.Errorf("%s arguments: %w", name, err)
			}

			if imageExtension(format) == "" {
				return codemode.NewError(name, fmt.Sprintf(`format must be "png", "jpeg" or "gif", got %q`, format)), nil
			}

			if quality < 1 || quality > 100 {
				return codemode.NewError(name, fmt.Sprintf("quality must be between 1 and 100, got %d", quality)), nil
			}

			return editImage(f, name, format, quality, func(img image.Image) (image.Image, string, string) {
				return img, name + " " + format, ""
			}), nil
		}
	default:
		return nil, nil
	}

	return starlark.NewBuiltin(name, edit), nil
}

func optionalImageSide(v starlark.Value, side string) (int, error) {
	if v == starlark.None {
		return 0, nil
	}

	n, err := starlark.AsInt32(v)
	if err != nil {
		return 0, fmt.Errorf("resize arguments: %s: %w", side, err)
	}

	return n, nil
}

func imageExtension(format string) string {
	switch format {
	case "png":
		return ".png"
	case "jpeg":
		return ".jpg"
	case "gif":
		return ".gif"
	default:
		return ""
	}
}

func imageTooLarge(width, height int) string {
	return fmt.Sprintf("image too large to edit (%d × %d pixels; limit 10,240,000)", width, height)
}

func editImage(f *codemode.File, op, format string, quality int, transform func(image.Image) (image.Image, string, string)) starlark.Value {
	if !strings.HasPrefix(f.Mime(), "image/") {
		return codemode.NewError(op, f.Mime()+" is not an image")
	}

	width, height, ok := f.Dimensions()
	if !ok {
		return codemode.NewError(op, "cannot decode "+f.Mime()+" image")
	}

	if width*height > maxEditPixels {
		return codemode.NewError(op, imageTooLarge(width, height))
	}

	img, inputFormat, errDecode := image.Decode(strings.NewReader(f.Content()))
	if errDecode != nil {
		return codemode.NewError(op, "cannot decode "+f.Mime()+" image: "+errDecode.Error())
	}

	edited, step, failure := transform(img)
	if failure != "" {
		return codemode.NewError(op, failure)
	}

	if format == "" {
		format = inputFormat
		if imageExtension(format) == "" {
			format = "png"
		}
	}

	var (
		out       strings.Builder
		errEncode error
	)

	switch format {
	case "jpeg":
		errEncode = jpeg.Encode(&out, edited, &jpeg.Options{Quality: quality})
	case "gif":
		errEncode = gif.Encode(&out, edited, nil)
	default:
		errEncode = png.Encode(&out, edited)
	}

	if errEncode != nil {
		return codemode.NewError(op, errEncode.Error())
	}

	name := f.Name()
	if format != inputFormat {
		name = strings.TrimSuffix(name, path.Ext(name)) + imageExtension(format)
	}

	return newFileValue(codemode.FileSpec{
		Name:    name,
		Mime:    "image/" + format,
		Content: out.String(),
		Source:  strings.TrimPrefix(f.Source()+" → "+step, " → "),
	})
}

func rotateImage(img image.Image, degrees int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	rotated := image.Rect(0, 0, h, w)
	if degrees == 180 {
		rotated = image.Rect(0, 0, w, h)
	}

	var (
		out                  image.Image
		src, dst             []byte
		srcStride, dstStride int
		bpp                  int
	)

	if p, ok := img.(*image.Paletted); ok {
		d := image.NewPaletted(rotated, p.Palette)
		out, src, srcStride, dst, dstStride, bpp = d, p.Pix[p.PixOffset(b.Min.X, b.Min.Y):], p.Stride, d.Pix, d.Stride, 1
	} else {
		s := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.Draw(s, s.Bounds(), img, b.Min, xdraw.Src)

		d := image.NewRGBA(rotated)
		out, src, srcStride, dst, dstStride, bpp = d, s.Pix, s.Stride, d.Pix, d.Stride, 4
	}

	for y := range h {
		for x := range w {
			dx, dy := h-1-y, x

			switch degrees {
			case 180:
				dx, dy = w-1-x, h-1-y
			case 270:
				dx, dy = y, w-1-x
			}

			copy(dst[dy*dstStride+dx*bpp:][:bpp], src[y*srcStride+x*bpp:][:bpp])
		}
	}

	return out
}
