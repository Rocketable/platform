package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type quadrant int

const (
	topLeft quadrant = iota
	topRight
	bottomLeft
	bottomRight
)

func exifTIFF(order binary.AppendByteOrder, orientation int) []byte {
	tiff := []byte("II*\x00")
	if order == binary.BigEndian {
		tiff = []byte("MM\x00*")
	}

	tiff = order.AppendUint32(tiff, 8)
	tiff = order.AppendUint16(tiff, 2)
	tiff = order.AppendUint16(tiff, 0x010f)
	tiff = order.AppendUint16(tiff, 2)
	tiff = order.AppendUint32(tiff, 4)
	tiff = append(tiff, "Cam\x00"...)
	tiff = order.AppendUint16(tiff, 0x0112)
	tiff = order.AppendUint16(tiff, 3)
	tiff = order.AppendUint32(tiff, 1)
	tiff = order.AppendUint16(tiff, uint16(orientation))

	return append(tiff, 0, 0, 0, 0, 0, 0)
}

func quadrantImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	colors := map[quadrant]color.RGBA{topLeft: {R: 0xff, A: 0xff}, topRight: {G: 0xff, A: 0xff}, bottomLeft: {B: 0xff, A: 0xff}, bottomRight: {R: 0xff, G: 0xff, B: 0xff, A: 0xff}}

	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, colors[quadrant(2*(2*y/height)+2*x/width)])
		}
	}

	return img
}

func exifTaggedJPEG(t *testing.T, img image.Image, order binary.AppendByteOrder, orientation int) []byte {
	t.Helper()

	encoded := encodeAttachmentTestJPEG(t, img, 95)

	app1 := append([]byte("Exif\x00\x00"), exifTIFF(order, orientation)...)
	segment := binary.BigEndian.AppendUint16([]byte{0xff, 0xe1}, uint16(len(app1)+2))

	return append(append(append([]byte{0xff, 0xd8}, segment...), app1...), encoded[2:]...)
}

func withJPEGSize(t *testing.T, data []byte, width, height int) []byte {
	t.Helper()

	sof := bytes.Index(data, []byte{0xff, 0xc0})
	require.Positive(t, sof)

	out := bytes.Clone(data)
	binary.BigEndian.PutUint16(out[sof+5:], uint16(height))
	binary.BigEndian.PutUint16(out[sof+7:], uint16(width))

	return out
}

func exifTaggedWebP(t *testing.T, exif []byte) []byte {
	t.Helper()

	lossy, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)

	vp8x := append([]byte("VP8X\x0a\x00\x00\x00\x08"), make([]byte, 9)...)
	body := append(append(append([]byte("WEBP"), vp8x...), lossy[12:]...), "EXIF"...)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(exif)))

	body = append(body, exif...)
	if len(exif)%2 == 1 {
		body = append(body, 0)
	}

	return append(binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body))), body...)
}

func TestStraightenImageAppliesEveryEXIFOrientation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		orientation   int
		width, height int
		red, green    quadrant
		order         binary.AppendByteOrder
	}{
		{orientation: 2, width: 64, height: 32, red: topRight, green: topLeft, order: binary.LittleEndian},
		{orientation: 3, width: 64, height: 32, red: bottomRight, green: bottomLeft, order: binary.BigEndian},
		{orientation: 4, width: 64, height: 32, red: bottomLeft, green: bottomRight, order: binary.LittleEndian},
		{orientation: 5, width: 32, height: 64, red: topLeft, green: bottomLeft, order: binary.BigEndian},
		{orientation: 6, width: 32, height: 64, red: topRight, green: bottomRight, order: binary.LittleEndian},
		{orientation: 7, width: 32, height: 64, red: bottomRight, green: topRight, order: binary.BigEndian},
		{orientation: 8, width: 32, height: 64, red: bottomLeft, green: topLeft, order: binary.LittleEndian},
	} {
		t.Run(strconv.Itoa(tc.orientation), func(t *testing.T) {
			t.Parallel()

			mimeType, upright, err := StraightenImage("image/jpeg", string(exifTaggedJPEG(t, quadrantImage(64, 32), tc.order, tc.orientation)))
			require.NoError(t, err)
			assert.Equal(t, "image/jpeg", mimeType)
			assert.Equal(t, 1, exifOrientation(upright))

			img, format, err := image.Decode(bytes.NewReader([]byte(upright)))
			require.NoError(t, err)
			assert.Equal(t, "jpeg", format)
			require.Equal(t, image.Rect(0, 0, tc.width, tc.height), img.Bounds())

			at := func(q quadrant) color.RGBA {
				x, y := tc.width/4+tc.width/2*(int(q)%2), tc.height/4+tc.height/2*(int(q)/2)
				r, g, b, _ := img.At(x, y).RGBA()

				return color.RGBA{R: uint8(r >> 15 * 0xff), G: uint8(g >> 15 * 0xff), B: uint8(b >> 15 * 0xff), A: 0xff}
			}
			assert.Equal(t, color.RGBA{R: 0xff, A: 0xff}, at(tc.red))
			assert.Equal(t, color.RGBA{G: 0xff, A: 0xff}, at(tc.green))
		})
	}
}

func TestStraightenImageLeavesUprightImagesUntouched(t *testing.T) {
	t.Parallel()

	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, quadrantImage(4, 2)))

	untagged := encodeAttachmentTestJPEG(t, quadrantImage(4, 2), 90)
	upright := exifTaggedJPEG(t, quadrantImage(4, 2), binary.BigEndian, 1)

	for name, input := range map[string]struct {
		mimeType string
		data     []byte
	}{
		"orientation 1":                 {"image/jpeg", upright},
		"orientation 1 over the cap":    {"image/jpeg", withJPEGSize(t, upright, 4000, 3000)},
		"no EXIF":                       {"image/jpeg", untagged},
		"out-of-range orientation":      {"image/jpeg", exifTaggedJPEG(t, quadrantImage(4, 2), binary.LittleEndian, 9)},
		"png":                           {"image/png", pngData.Bytes()},
		"webp without EXIF orientation": {"image/webp", exifTaggedWebP(t, exifTIFF(binary.LittleEndian, 1))},
		"pdf":                           {"application/pdf", []byte("%PDF-1.4")},
	} {
		mimeType, out, err := StraightenImage(input.mimeType, string(input.data))
		require.NoError(t, err, name)
		assert.Equal(t, input.mimeType, mimeType, name)
		assert.Equal(t, string(input.data), out, name)
	}
}

func TestStraightenImageTurnsWebPIntoJPEG(t *testing.T) {
	t.Parallel()

	for name, exif := range map[string][]byte{
		"bare TIFF":         exifTIFF(binary.LittleEndian, 6),
		"Exif\\0\\0 prefix": append([]byte("Exif\x00\x00"), exifTIFF(binary.BigEndian, 6)...),
	} {
		mimeType, upright, err := StraightenImage("image/webp", string(exifTaggedWebP(t, exif)))
		require.NoError(t, err, name)
		assert.Equal(t, "image/jpeg", mimeType, name)

		cfg, format, err := image.DecodeConfig(bytes.NewReader([]byte(upright)))
		require.NoError(t, err, name)
		assert.Equal(t, "jpeg", format, name)
		assert.Equal(t, [2]int{1, 1}, [2]int{cfg.Width, cfg.Height}, name)
	}
}

func TestStraightenImageRefusesTurnedImagesOverTheCap(t *testing.T) {
	t.Parallel()

	tagged := exifTaggedJPEG(t, quadrantImage(4, 2), binary.LittleEndian, 6)

	for _, tc := range []struct {
		width, height int
		want          string
	}{
		{width: 4000, height: 3000, want: "image too large to straighten (12 MP; limit 10.24 MP)"},
		{width: 3201, height: 3200, want: "image too large to straighten (10.25 MP; limit 10.24 MP)"},
		{width: 8000, height: 6000, want: "image too large to straighten (48 MP; limit 10.24 MP)"},
	} {
		_, _, err := StraightenImage("image/jpeg", string(withJPEGSize(t, tagged, tc.width, tc.height)))
		require.EqualError(t, err, tc.want)
	}

	_, _, err := StraightenImage("image/jpeg", string(withJPEGSize(t, tagged, 3200, 3200)))
	require.ErrorContains(t, err, "image could not be straightened: ")

	_, _, err = StraightenImage("image/jpeg", string(tagged[:len(tagged)-200]))
	require.ErrorContains(t, err, "image could not be straightened: ")
}

func TestEXIFOrientationSurvivesTruncatedInput(t *testing.T) {
	t.Parallel()

	for _, data := range [][]byte{
		exifTaggedJPEG(t, quadrantImage(4, 2), binary.LittleEndian, 6),
		exifTaggedJPEG(t, quadrantImage(4, 2), binary.BigEndian, 8),
		exifTaggedWebP(t, exifTIFF(binary.LittleEndian, 6)),
	} {
		for n := range min(len(data), 200) {
			got := exifOrientation(string(data[:n]))
			assert.Contains(t, []int{1, 6, 8}, got)
		}
	}

	assert.Equal(t, 1, exifOrientation("\xff\xd8\xff\xe1\x00\x00Exif\x00\x00"))
	assert.Equal(t, 1, exifOrientation("\xff\xd8\xff\xe1\x00\x10Exif\x00\x00MM\x00*\xff\xff\xff\xff"))
}

func TestNormalizeInboundAttachmentsStraightensImages(t *testing.T) {
	t.Parallel()

	tagged := exifTaggedJPEG(t, quadrantImage(64, 32), binary.LittleEndian, 6)
	content := &InboundContent{Attachments: []InboundAttachment{
		{Name: "photo.jpg", MIMEType: "image/jpeg", Data: tagged},
		{Name: "big.jpg", MIMEType: "image/jpeg", Data: withJPEGSize(t, tagged, 4000, 3000)},
	}}

	normalizeInboundAttachments(content)

	require.Len(t, content.Attachments, 1)
	assert.Equal(t, "image/jpeg", content.Attachments[0].MIMEType)
	cfg := decodeAttachmentTestImageConfig(t, content.Attachments[0].Data)
	assert.Equal(t, [2]int{32, 64}, [2]int{cfg.Width, cfg.Height})
	assert.Equal(t, []string{"Skipped attachment big.jpg because image too large to straighten (12 MP; limit 10.24 MP)."}, content.AttachmentWarnings)
}
