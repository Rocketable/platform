package protocol

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"slices"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	maxInboundAttachmentBytes          = 4 << 20
	maxInboundAttachmentTotalBytes     = 16 << 20
	maxInboundAttachmentResizeInput    = 16 << 20
	maxInboundAttachmentResizeAttempts = 8
)

var errInboundAttachmentReductionFailed = errors.New("inbound attachment image reduction failed")

var errInboundAttachmentReductionNotEnough = errors.New("inbound attachment image still exceeds size limit after reduction")

func normalizeInboundAttachments(content *InboundContent) {
	if len(content.Attachments) == 0 {
		return
	}

	content.AttachmentPresence = AttachmentPresenceImages
	attachments := make([]InboundAttachment, 0, len(content.Attachments))
	totalBytes := 0

	for i := range content.Attachments {
		attachment := content.Attachments[i]

		name := cmp.Or(strings.TrimSpace(attachment.Name), fmt.Sprintf("attachment-%d", i+1))

		data := attachment.Data

		if len(data) == 0 {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped attachment "+name+" because it was empty.")
			continue
		}

		mimeType := NormalizeMIMEType(http.DetectContentType(data))
		if !slices.Contains([]string{"image/jpeg", "image/jpg", "image/png", "image/webp"}, mimeType) {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped attachment "+name+" because "+mimeType+" is not supported.")
			continue
		}

		targetLimit := min(maxInboundAttachmentBytes, maxInboundAttachmentTotalBytes-totalBytes)
		if targetLimit <= 0 {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped attachment "+name+" because the message exceeded the attachment size budget.")
			continue
		}

		if len(data) > maxInboundAttachmentResizeInput {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped attachment "+name+" because it was too large to attempt size reduction.")
			continue
		}

		mimeType, upright, err := StraightenImage(mimeType, string(data))
		if err != nil {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped attachment "+name+" because "+err.Error()+".")
			continue
		}

		data, mimeType, err = fitInboundImageWithinLimit(mimeType, []byte(upright), targetLimit)
		if err != nil {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped attachment "+name+" because "+inboundAttachmentReductionFailureReason(err, targetLimit)+".")
			continue
		}

		totalBytes += len(data)
		attachments = append(attachments, InboundAttachment{Name: name, MIMEType: mimeType, Data: data})
	}

	content.Attachments = attachments
}

func fitInboundImageWithinLimit(mimeType string, data []byte, targetLimit int) (transformedData []byte, transformedMIMEType string, err error) {
	if len(data) <= targetLimit {
		return slices.Clone(data), mimeType, nil
	}

	var img image.Image
	if mimeType == "image/png" {
		if img, err = png.Decode(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("%w: decode png: %w", errInboundAttachmentReductionFailed, err)
		}

		transformed, err := shrinkImageWithinLimit(img, targetLimit, inboundPNGEncoder{})
		if err == nil {
			return transformed, mimeType, nil
		}

		if !errors.Is(err, errInboundAttachmentReductionNotEnough) {
			return nil, "", err
		}
	} else if img, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", fmt.Errorf("%w: decode image: %w", errInboundAttachmentReductionFailed, err)
	}

	candidate, err := shrinkImageWithinLimit(flattenInboundImageForJPEG(img), targetLimit, inboundJPEGEncoder{})
	if err != nil {
		return nil, "", err
	}

	return candidate, "image/jpeg", nil
}

type inboundImageEncoder interface {
	encodeWithinLimit(image.Image, int) ([]byte, int, error)
}

func shrinkImageWithinLimit(src image.Image, targetLimit int, encoder inboundImageEncoder) ([]byte, error) {
	candidate, currentSize, err := encoder.encodeWithinLimit(src, targetLimit)
	if err != nil || candidate != nil {
		return candidate, err
	}

	bounds := src.Bounds()

	currentWidth, currentHeight := bounds.Dx(), bounds.Dy()
	for range maxInboundAttachmentResizeAttempts {
		if currentWidth <= 1 || currentHeight <= 1 {
			break
		}

		nextWidth, nextHeight := nextImageResizeDimensions(currentWidth, currentHeight, currentSize, targetLimit)
		if nextWidth >= currentWidth && nextHeight >= currentHeight {
			break
		}

		resized := image.NewNRGBA(image.Rect(0, 0, nextWidth, nextHeight))
		xdraw.CatmullRom.Scale(resized, resized.Bounds(), src, bounds, xdraw.Over, nil)

		candidate, currentSize, err = encoder.encodeWithinLimit(resized, targetLimit)
		if err != nil || candidate != nil {
			return candidate, err
		}

		currentWidth, currentHeight = nextWidth, nextHeight
	}

	return nil, errInboundAttachmentReductionNotEnough
}

func flattenInboundImageForJPEG(src image.Image) *image.NRGBA {
	bounds := src.Bounds()

	flattened := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(flattened, flattened.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(flattened, flattened.Bounds(), src, bounds.Min, draw.Over)

	return flattened
}

type inboundJPEGEncoder struct{}

func (inboundJPEGEncoder) encodeWithinLimit(img image.Image, targetLimit int) (candidate []byte, candidateSize int, err error) {
	bestSize := math.MaxInt

	for quality := 95; quality >= 50; quality -= 5 {
		var buffer bytes.Buffer
		if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, 0, fmt.Errorf("%w: encode jpeg: %w", errInboundAttachmentReductionFailed, fmt.Errorf("encode jpeg: %w", err))
		}

		bestSize = min(bestSize, buffer.Len())
		if buffer.Len() <= targetLimit {
			return buffer.Bytes(), buffer.Len(), nil
		}
	}

	return nil, bestSize, nil
}

type inboundPNGEncoder struct{}

func (inboundPNGEncoder) encodeWithinLimit(img image.Image, targetLimit int) (candidate []byte, candidateSize int, err error) {
	var buffer bytes.Buffer

	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	_ = encoder.Encode(&buffer, img)

	if buffer.Len() <= targetLimit {
		return buffer.Bytes(), buffer.Len(), nil
	}

	return nil, buffer.Len(), nil
}

func nextImageResizeDimensions(currentWidth, currentHeight, currentSize, targetLimit int) (nextWidth, nextHeight int) {
	scale := math.Sqrt(float64(targetLimit)/float64(currentSize)) * 0.92
	if scale >= 1 {
		scale = 0.92
	}

	nextWidth = min(max(1, int(math.Round(float64(currentWidth)*scale))), max(1, currentWidth-1))
	nextHeight = min(max(1, int(math.Round(float64(currentHeight)*scale))), max(1, currentHeight-1))

	return nextWidth, nextHeight
}

func inboundAttachmentReductionFailureReason(err error, targetLimit int) string {
	if errors.Is(err, errInboundAttachmentReductionFailed) {
		return "image reduction failed"
	}

	if targetLimit < maxInboundAttachmentBytes {
		return "it still exceeded the remaining attachment budget after reduction"
	}

	return "it still exceeded the per-file size limit after reduction"
}
