package localstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteJSONReplacesPrivateFileWithoutTemporaryArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	type state struct {
		Version int    `json:"version"`
		Value   string `json:"value"`
	}
	want := state{Version: 1, Value: "current"}
	if err := WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got state
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("state mode = %o, want 600", gotMode)
		}
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".kigo-state-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestWithFileLockRemovesStaleLockAndSerializesCallers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := WithFileLock(path, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("stale lock remains: %v", err)
	}

	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- WithFileLock(path, func() error {
			close(firstEntered)
			<-firstRelease
			return nil
		})
	}()
	<-firstEntered
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- WithFileLock(path, func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second caller entered while the first held the file lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	<-secondEntered
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}
