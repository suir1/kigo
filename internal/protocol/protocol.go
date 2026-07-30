package protocol

import (
	"encoding/json"
	"time"
)

const (
	Version   = 1
	ChunkSize = 64 * 1024
)

type ItemKind string

const (
	ItemFile      ItemKind = "file"
	ItemText      ItemKind = "text"
	ItemDirectory ItemKind = "directory"
	ItemSymlink   ItemKind = "symlink"
)

type Item struct {
	Kind            ItemKind `json:"kind"`
	Name            string   `json:"name"`
	Size            int64    `json:"size"`
	MTime           int64    `json:"mtime,omitempty"`
	Mode            uint32   `json:"mode,omitempty"`
	SHA256          string   `json:"sha256,omitempty"`
	SampleSHA256    string   `json:"sample_sha256,omitempty"`
	Target          string   `json:"target,omitempty"`
	ChunkSize       int      `json:"chunk_size"`
	ResumeSupported bool     `json:"resume_supported"`
}

type Manifest struct {
	Version int             `json:"version"`
	Items   []Item          `json:"items"`
	Streams []StreamBinding `json:"streams,omitempty"`
}

type StreamBinding struct {
	ID   int `json:"id"`
	Item int `json:"item"`
}

type ResumeEntry struct {
	Item         int    `json:"item"`
	Stream       *int   `json:"stream,omitempty"`
	Offset       int64  `json:"offset"`
	PrefixSHA256 string `json:"prefix_sha256,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Skip         bool   `json:"skip,omitempty"`
	Complete     bool   `json:"complete,omitempty"`
}

type Message struct {
	Type     string          `json:"type"`
	Version  int             `json:"version,omitempty"`
	Manifest *Manifest       `json:"manifest,omitempty"`
	Resume   []ResumeEntry   `json:"resume,omitempty"`
	Item     int             `json:"item"`
	Stream   *int            `json:"stream,omitempty"`
	Offset   int64           `json:"offset"`
	Data     string          `json:"data,omitempty"`
	Encoding string          `json:"encoding,omitempty"`
	Error    string          `json:"error,omitempty"`
	At       int64           `json:"at,omitempty"`
	Extra    json.RawMessage `json:"extra,omitempty"`
}

func NewManifest(items []Item) Manifest {
	return Manifest{Version: Version, Items: items}
}

func ValidSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func NowMillis() int64 {
	return time.Now().UnixMilli()
}
