package protocol

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"math"
	"strconv"
	"strings"
)

const maxStraightenPixels = 10_240_000

func StraightenImage(mimeType, data string) (uprightMIMEType, upright string, err error) {
	orientation := exifOrientation(data)
	if orientation == 1 {
		return mimeType, data, nil
	}

	cfg, _, err := image.DecodeConfig(strings.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("image could not be straightened: %w", err)
	}

	if pixels := cfg.Width * cfg.Height; pixels > maxStraightenPixels {
		return "", "", fmt.Errorf("image too large to straighten (%s MP; limit 10.24 MP)", strconv.FormatFloat(math.Ceil(float64(pixels)/10_000)/100, 'f', -1, 64))
	}

	img, _, err := image.Decode(strings.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("image could not be straightened: %w", err)
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(src, src.Bounds(), img, bounds.Min, draw.Over)

	rect := src.Bounds()
	if orientation >= 5 {
		rect = image.Rect(0, 0, h, w)
	}

	dst := image.NewRGBA(rect)

	for y := range h {
		for x := range w {
			dx, dy := x, y

			switch orientation {
			case 2:
				dx = w - 1 - x
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dy = h - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}

			copy(dst.Pix[dst.PixOffset(dx, dy):][:4], src.Pix[src.PixOffset(x, y):][:4])
		}
	}

	var out strings.Builder
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 90}); err != nil {
		return "", "", fmt.Errorf("image could not be straightened: %w", err)
	}

	return "image/jpeg", out.String(), nil
}

func exifOrientation(data string) int {
	switch {
	case strings.HasPrefix(data, "\xff\xd8"):
		for i := 2; i+4 <= len(data) && data[i] == 0xff; {
			marker := data[i+1]
			if marker == 0xff {
				i++
				continue
			}

			end := min(i+2+int(binary.BigEndian.Uint16([]byte(data[i+2:i+4]))), len(data))
			if marker == 0xda || marker == 0xd9 || end < i+4 {
				break
			}

			if marker == 0xe1 && strings.HasPrefix(data[i+4:end], "Exif\x00\x00") {
				return tiffOrientation(data[i+10 : end])
			}

			i = end
		}
	case len(data) >= 12 && data[:4] == "RIFF" && data[8:12] == "WEBP":
		for i := 12; i+8 <= len(data); {
			size := int(binary.LittleEndian.Uint32([]byte(data[i+4 : i+8])))
			if data[i:i+4] == "EXIF" {
				return tiffOrientation(strings.TrimPrefix(data[i+8:min(i+8+size, len(data))], "Exif\x00\x00"))
			}

			i += 8 + size + size%2
		}
	}

	return 1
}

func tiffOrientation(tiff string) int {
	var order binary.ByteOrder = binary.BigEndian

	switch {
	case len(tiff) < 8:
		return 1
	case strings.HasPrefix(tiff, "II*\x00"):
		order = binary.LittleEndian
	case !strings.HasPrefix(tiff, "MM\x00*"):
		return 1
	}

	u16 := func(i int) int { return int(order.Uint16([]byte(tiff[i : i+2]))) }

	ifd := int(order.Uint32([]byte(tiff[4:8])))
	if ifd+2 > len(tiff) {
		return 1
	}

	for i := range u16(ifd) {
		entry := ifd + 2 + 12*i
		if entry+12 > len(tiff) {
			return 1
		}

		if u16(entry) == 0x0112 {
			if orientation := u16(entry + 8); orientation >= 1 && orientation <= 8 {
				return orientation
			}

			return 1
		}
	}

	return 1
}
