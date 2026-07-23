package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

const (
	sampleHashChunkSize = int64(128 * 1024)
	sampleHashWindow    = int64(1024 * 1024)
	sampleHashMaxBytes  = int64(1024 * 1024)
)

// fileSampleSHA256 is an imohash-style fingerprint. It reads the whole file
// when small and bounded head, tail, and interior samples when large.
func fileSampleSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("sample hash requires a regular file")
	}
	return sampleSHA256(file, info.Size())
}

func sampleSHA256(reader io.ReaderAt, size int64) (string, error) {
	if reader == nil {
		return "", errors.New("sample hash reader is nil")
	}
	if size < 0 {
		return "", errors.New("sample hash size cannot be negative")
	}
	hash := sha256.New()
	readSample := func(offset, length int64) error {
		if offset < 0 || length < 0 || offset > size || length > size-offset {
			return errors.New("sample hash range exceeds file size")
		}
		_, err := io.CopyN(hash, io.NewSectionReader(reader, offset, length), length)
		return err
	}
	if size <= sampleHashChunkSize*2 {
		if err := readSample(0, size); err != nil {
			return "", err
		}
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	if err := readSample(0, sampleHashChunkSize); err != nil {
		return "", err
	}
	if err := readSample(size-sampleHashChunkSize, sampleHashChunkSize); err != nil {
		return "", err
	}
	sampled := sampleHashChunkSize * 2
	windows := max(int64(1), (size-sampleHashChunkSize)/sampleHashWindow)
	for window := int64(0); window < windows && sampled < sampleHashMaxBytes; window++ {
		offset := window*sampleHashWindow + sampleHashWindow/2
		if offset+sampleHashChunkSize > size {
			break
		}
		if err := readSample(offset, sampleHashChunkSize); err != nil {
			return "", err
		}
		sampled += sampleHashChunkSize
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
