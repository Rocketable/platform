package protocol

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeInboundAttachmentsCentralizesModelPolicy(t *testing.T) {
	t.Parallel()

	first := tinyPNG()
	content := &InboundContent{Attachments: []InboundAttachment{
		{Name: " tiny.png ", MIMEType: "application/octet-stream", Data: first},
		{Name: "not-image.png", MIMEType: "image/png", Data: []byte("not an image")},
		{Name: "empty.png", MIMEType: "image/png"},
		{Name: "photo.jpg", MIMEType: " Image/PNG; Charset=UTF-8 "},
		{Name: "photo.jpg", MIMEType: " "},
		{Name: "photo", MIMEType: " "},
		{Name: " ", MIMEType: "image/png"},
		{Name: "second.png", MIMEType: "image/jpeg", Data: tinyPNG()},
	}}

	normalizeInboundAttachments(content)
	clear(first)

	assert.Equal(t, AttachmentPresenceImages, content.AttachmentPresence)
	assert.Equal(t, []InboundAttachment{
		{Name: "tiny.png", MIMEType: "image/png", Data: tinyPNG()},
		{Name: "second.png", MIMEType: "image/png", Data: tinyPNG()},
	}, content.Attachments)
	assert.Equal(t, []string{
		"Skipped attachment not-image.png because text/plain is not supported.",
		"Skipped attachment empty.png because it was empty.",
		"Skipped attachment photo.jpg because it was empty.",
		"Skipped attachment photo.jpg because it was empty.",
		"Skipped attachment photo because it was empty.",
		"Skipped attachment attachment-7 because it was empty.",
	}, content.AttachmentWarnings)
}

func TestNormalizeInboundAttachmentsRejectsAttachmentsTooLargeToReduce(t *testing.T) {
	t.Parallel()

	content := &InboundContent{Attachments: []InboundAttachment{
		{Name: "large.png", MIMEType: "image/png", Data: append(tinyPNG(), make([]byte, maxInboundAttachmentResizeInput)...)},
	}}

	normalizeInboundAttachments(content)

	assert.Empty(t, content.Attachments)
	assert.Equal(t, AttachmentPresenceImages, content.AttachmentPresence)
	assert.Equal(t, []string{"Skipped attachment large.png because it was too large to attempt size reduction."}, content.AttachmentWarnings)
}

func TestShrinkImageWithinLimitReportsEncoderErrorsAndExhaustion(t *testing.T) {
	t.Parallel()

	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	_, err := shrinkImageWithinLimit(img, 1, &inboundImageEncoderMock{encodeWithinLimitFunc: func(image.Image, int) ([]byte, int, error) {
		return nil, 0, assert.AnError
	}})
	require.ErrorIs(t, err, assert.AnError)

	_, err = shrinkImageWithinLimit(img, 1, &inboundImageEncoderMock{encodeWithinLimitFunc: func(image.Image, int) ([]byte, int, error) {
		return nil, 1000, nil
	}})
	require.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestFitInboundImageWithinLimitLeavesSmallImageUnchanged(t *testing.T) {
	t.Parallel()

	data := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.BestCompression)

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/png", data, len(data))
	require.NoError(t, err)
	assert.Equal(t, data, transformed)
	assert.Same(t, &data[0], &transformed[0])
	assert.Equal(t, "image/png", transformedMIMEType)
}

func TestFitInboundImageWithinLimitUsesLosslessPNGFirst(t *testing.T) {
	t.Parallel()

	img := newAttachmentTestImage(160, 160)
	original := encodeAttachmentTestPNG(t, img, png.NoCompression)
	target := len(encodeAttachmentTestPNG(t, img, png.BestCompression))
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/png", original, target)
	require.NoError(t, err)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Equal(t, originalConfig.Width, transformedConfig.Width)
	assert.Equal(t, originalConfig.Height, transformedConfig.Height)
}

func TestFitInboundImageWithinLimitUsesSmallestSuccessfulJPEGChangeFirst(t *testing.T) {
	t.Parallel()

	img := newAttachmentTestImage(256, 256)
	original := encodeAttachmentTestJPEG(t, img, 100)
	target := 0

	for quality := 95; quality >= 50; quality -= 5 {
		candidateSize := len(encodeAttachmentTestJPEG(t, img, quality))
		if candidateSize < len(original) {
			target = candidateSize
			break
		}
	}

	require.NotZero(t, target)

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/jpeg", original, target)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Equal(t, originalConfig.Width, transformedConfig.Width)
	assert.Equal(t, originalConfig.Height, transformedConfig.Height)
}

func TestFitInboundImageWithinLimitResizesPNG(t *testing.T) {
	t.Parallel()

	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(400, 400), png.NoCompression)
	target := 4096
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/png", original, target)
	require.NoError(t, err)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Less(t, transformedConfig.Width, originalConfig.Width)
	assert.Less(t, transformedConfig.Height, originalConfig.Height)
}

func TestFitInboundImageWithinLimitFallsBackToJPEG(t *testing.T) {
	t.Parallel()

	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(80, 80), png.NoCompression)
	target := 1024
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/webp", original, target)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	cfg := decodeAttachmentTestImageConfig(t, transformed)
	assert.NotZero(t, cfg.Width)
	assert.NotZero(t, cfg.Height)

	oversized := encodeAttachmentTestPNG(t, newAttachmentTestImage(65536, 1), png.NoCompression)
	transformed, transformedMIMEType, err = fitInboundImageWithinLimit("image/webp", oversized, 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	require.ErrorIs(t, err, errInboundAttachmentReductionFailed)

	errJPEGText := "jpeg: image is too large to encode"
	require.EqualError(t, err, errInboundAttachmentReductionFailed.Error()+": "+strings.Repeat("encode jpeg: ", 2)+errJPEGText)
	chain, ok := errors.AsType[interface {
		Error() string
		Unwrap() []error
	}](err)
	require.True(t, ok)
	require.Len(t, chain.Unwrap(), 2)
	assert.Same(t, errInboundAttachmentReductionFailed, chain.Unwrap()[0])
	require.EqualError(t, chain.Unwrap()[1], "encode jpeg: "+errJPEGText)
	require.EqualError(t, errors.Unwrap(chain.Unwrap()[1]), errJPEGText)
}

func TestFlattenInboundImageForJPEGCompositesOnWhite(t *testing.T) {
	t.Parallel()

	img := image.NewNRGBA(image.Rect(5, 7, 8, 8))
	img.SetNRGBA(5, 7, color.NRGBA{R: 255, A: 128})
	img.SetNRGBA(6, 7, color.NRGBA{G: 255})
	img.SetNRGBA(7, 7, color.NRGBA{B: 255, A: 255})

	assert.Equal(t, []byte{255, 127, 127, 255, 255, 255, 255, 255, 0, 0, 255, 255}, flattenInboundImageForJPEG(img).Pix)
}

func TestFitInboundImageWithinLimitReportsDecodeFailure(t *testing.T) {
	t.Parallel()

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/webp", []byte("not an image"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestFitInboundImageWithinLimitReportsPNGDecodeFailure(t *testing.T) {
	t.Parallel()

	transformed, transformedMIMEType, err := fitInboundImageWithinLimit("image/png", []byte("not a png"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestShrinkPNGWithinLimitRejectsSinglePixelImageTooLargeForTarget(t *testing.T) {
	t.Parallel()

	transformed, err := shrinkImageWithinLimit(newAttachmentTestImage(1, 1), 1, inboundPNGEncoder{})
	assert.Nil(t, transformed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestNextImageResizeDimensionsStillShrinksWhenEstimateWouldGrow(t *testing.T) {
	t.Parallel()

	nextWidth, nextHeight := nextImageResizeDimensions(2, 2, 100, 10000)

	assert.Equal(t, 1, nextWidth)
	assert.Equal(t, 1, nextHeight)
}

func TestInboundAttachmentReductionFailureReason(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "image reduction failed", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionFailed, maxInboundAttachmentBytes))
	assert.Equal(t, "it still exceeded the remaining attachment budget after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, 1))
	assert.Equal(t, "it still exceeded the per-file size limit after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, maxInboundAttachmentBytes))
}

func TestNormalizeInboundAttachmentsRejectsTotalBudgetOverflow(t *testing.T) {
	t.Parallel()

	data := append(tinyPNG(), make([]byte, maxInboundAttachmentBytes-len(tinyPNG()))...)
	content := &InboundContent{Attachments: []InboundAttachment{
		{Name: "one.png", MIMEType: "image/png", Data: data},
		{Name: "two.png", MIMEType: "image/png", Data: data},
		{Name: "three.png", MIMEType: "image/png", Data: data},
		{Name: "four.png", MIMEType: "image/png", Data: data},
		{MIMEType: "image/png", Data: data},
	}}

	normalizeInboundAttachments(content)

	require.Len(t, content.Attachments, 4)
	assert.Equal(t, []string{"Skipped attachment attachment-5 because the message exceeded the attachment size budget."}, content.AttachmentWarnings)
}

func tinyPNG() []byte {
	return []byte("\x89PNG\r\n\x1a\n")
}

func newAttachmentTestImage(width, height int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.NRGBA{R: uint8((x*31 + y*17) % 256), G: uint8((x*13 + y*29) % 256), B: uint8((x*7 + y*19) % 256), A: 0xff})
		}
	}

	return img
}

func encodeAttachmentTestPNG(t *testing.T, img image.Image, level png.CompressionLevel) []byte {
	t.Helper()

	var buffer bytes.Buffer

	encoder := png.Encoder{CompressionLevel: level, BufferPool: nil}
	require.NoError(t, encoder.Encode(&buffer, img))

	return buffer.Bytes()
}

func encodeAttachmentTestJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()

	var buffer bytes.Buffer

	options := jpeg.Options{Quality: quality}
	require.NoError(t, jpeg.Encode(&buffer, img, &options))

	return buffer.Bytes()
}

func decodeAttachmentTestImageConfig(t *testing.T, data []byte) image.Config {
	t.Helper()

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)

	return cfg
}
