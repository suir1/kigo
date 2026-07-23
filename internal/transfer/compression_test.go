package transfer

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/suir1/kigo/internal/protocol"
)

func TestGzipChunkRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("compressible payload\n"), 3000)
	encoded, err := compressGzipChunk(data)
	if err != nil {
		t.Fatal(err)
	}
	if !compressionWorthwhile(len(data), len(encoded)) {
		t.Fatalf("gzip did not produce useful savings: %d -> %d", len(data), len(encoded))
	}
	decoded, err := decodeTransferChunk(compressionGzip, compressionGzip, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("gzip round trip changed data")
	}
}

func TestCompressionDisablesAfterThreeMisses(t *testing.T) {
	session := &TransferSession{
		pipe:             &securePipe{compression: compressionGzip},
		compressionState: map[int]*chunkCompressionState{},
	}
	data := make([]byte, protocol.ChunkSize)
	for i := 0; i < 3; i++ {
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		_, encoding, err := session.encodeChunk(4, data)
		if err != nil {
			t.Fatal(err)
		}
		if encoding != "" {
			t.Fatalf("random chunk %d was compressed", i)
		}
	}
	if !session.compressionState[4].disabled {
		t.Fatal("compression was not disabled after three misses")
	}
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	_, encoding, err := session.encodeChunk(4, data)
	if err != nil {
		t.Fatal(err)
	}
	if encoding != "" {
		t.Fatal("disabled stream attempted compression")
	}
}

func TestDecodeTransferChunkRejectsUnnegotiatedAndUnknownEncoding(t *testing.T) {
	encoded, err := compressGzipChunk([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTransferChunk("", compressionGzip, encoded); err == nil {
		t.Fatal("unnegotiated gzip chunk was accepted")
	}
	if _, err := decodeTransferChunk(compressionGzip, "zstd", encoded); err == nil {
		t.Fatal("unknown chunk encoding was accepted")
	}
}

func TestDecompressGzipChunkRejectsOversizedOutput(t *testing.T) {
	encoded, err := compressGzipChunk(bytes.Repeat([]byte("x"), protocol.ChunkSize+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decompressGzipChunk(encoded); err == nil {
		t.Fatal("oversized decompressed chunk was accepted")
	}
}

func TestDecompressGzipChunkRejectsCorruptData(t *testing.T) {
	if _, err := decompressGzipChunk([]byte("not gzip")); err == nil {
		t.Fatal("corrupt gzip chunk was accepted")
	}
}
