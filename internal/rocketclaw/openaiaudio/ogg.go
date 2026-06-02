package openaiaudio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// DecodeOggOpus reads an Ogg stream and emits each Opus packet payload.
func DecodeOggOpus(r io.Reader, onFrame func([]byte) error) error {
	header, segment := make([]byte, 27), make([]byte, 255)

	var packet bytes.Buffer

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}

			return fmt.Errorf("read ogg header: %w", err)
		}

		if !bytes.Equal(header[:4], []byte("OggS")) {
			return errors.New("invalid ogg magic string")
		}

		pageSegments := int(header[26])

		segmentTable := make([]byte, pageSegments)
		if _, err := io.ReadFull(r, segmentTable); err != nil {
			return fmt.Errorf("read segment table: %w", err)
		}

		for _, lacing := range segmentTable {
			if _, err := io.ReadFull(r, segment[:lacing]); err != nil {
				return fmt.Errorf("read segment data: %w", err)
			}

			packet.Write(segment[:lacing])

			if lacing == 255 {
				continue
			}

			packetBytes := packet.Bytes()
			if !bytes.HasPrefix(packetBytes, []byte("OpusHead")) && !bytes.HasPrefix(packetBytes, []byte("OpusTags")) {
				frame := append([]byte(nil), packetBytes...)
				if err := onFrame(frame); err != nil {
					return err
				}
			}

			packet.Reset()
		}
	}
}
