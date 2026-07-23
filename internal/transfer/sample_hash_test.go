package transfer

import (
	"bytes"
	"io"
	"testing"
)

type countingReaderAt struct {
	reader io.ReaderAt
	read   int64
}

func (r *countingReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	n, err := r.reader.ReadAt(buffer, offset)
	r.read += int64(n)
	return n, err
}

func TestSampleSHA256HashesSmallFileCompletely(t *testing.T) {
	data := []byte("small payload")
	got, err := sampleSHA256(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if want := bytesSHA256(data); got != want {
		t.Fatalf("sample hash=%q want=%q", got, want)
	}
}

func TestSampleSHA256ReadsAtMostOneMiB(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 2*1024*1024)
	reader := &countingReaderAt{reader: bytes.NewReader(data)}
	if _, err := sampleSHA256(reader, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if reader.read != sampleHashMaxBytes {
		t.Fatalf("sampled bytes=%d want=%d", reader.read, sampleHashMaxBytes)
	}
}

func TestSampleSHA256DetectsSampledChanges(t *testing.T) {
	data := bytes.Repeat([]byte{0x2a}, 4*1024*1024)
	original, err := sampleSHA256(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, int(sampleHashWindow + sampleHashWindow/2), len(data) - 1} {
		changed := append([]byte(nil), data...)
		changed[offset] ^= 0xff
		got, err := sampleSHA256(bytes.NewReader(changed), int64(len(changed)))
		if err != nil {
			t.Fatal(err)
		}
		if got == original {
			t.Fatalf("sampled change at offset %d was not detected", offset)
		}
	}
}

func TestSampleSHA256RejectsInvalidSize(t *testing.T) {
	if _, err := sampleSHA256(bytes.NewReader(nil), -1); err == nil {
		t.Fatal("negative sample size was accepted")
	}
}
