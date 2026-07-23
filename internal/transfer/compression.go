package transfer

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"github.com/suir1/kigo/internal/protocol"
)

const compressionGzip = "gzip"

type chunkCompressionState struct {
	attempts int
	misses   int
	disabled bool
}

type CompressionStats struct {
	OriginalBytes    int64
	WireBytes        int64
	CompressedChunks int64
}

func compressGzipChunk(data []byte) ([]byte, error) {
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decompressGzipChunk(data []byte) ([]byte, error) {
	if len(data) > protocol.ChunkSize*2 {
		return nil, errors.New("compressed chunk exceeds encoded size limit")
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid gzip chunk: %w", err)
	}
	decoded, readErr := io.ReadAll(io.LimitReader(reader, protocol.ChunkSize+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("invalid gzip chunk: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("invalid gzip chunk: %w", closeErr)
	}
	if len(decoded) > protocol.ChunkSize {
		return nil, errors.New("decompressed chunk exceeds protocol chunk size")
	}
	return decoded, nil
}

func decodeTransferChunk(negotiated, encoding string, data []byte) ([]byte, error) {
	switch encoding {
	case "":
		return data, nil
	case compressionGzip:
		if negotiated != compressionGzip {
			return nil, errors.New("received gzip chunk without negotiated compression")
		}
		return decompressGzipChunk(data)
	default:
		return nil, fmt.Errorf("unsupported chunk encoding %q", encoding)
	}
}

func compressionWorthwhile(original, encoded int) bool {
	if original < 256 {
		return false
	}
	minSavings := maxInt(32, original/100)
	return encoded+minSavings < original
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
