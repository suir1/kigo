package transport

import (
	"encoding/binary"
	"errors"
	"io"
)

const maxFrameSize = 8 * 1024 * 1024

func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFrameSize {
		return errors.New("frame too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxFrameSize {
		return nil, errors.New("frame too large")
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(r, payload)
	return payload, err
}
