package transfer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/protocol"
)

func TestReceiveStorePersistsMixedManifest(t *testing.T) {
	outputDir := t.TempDir()
	fileData := []byte("file payload")
	firstText := []byte("first")
	secondText := []byte("second")
	manifest := protocol.NewManifest([]protocol.Item{
		{
			Kind:      protocol.ItemText,
			Name:      "first.txt",
			Size:      int64(len(firstText)),
			SHA256:    bytesSHA256(firstText),
			ChunkSize: protocol.ChunkSize,
		},
		{
			Kind:            protocol.ItemFile,
			Name:            "bundle/data.bin",
			Size:            int64(len(fileData)),
			SHA256:          bytesSHA256(fileData),
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		},
		{
			Kind:      protocol.ItemText,
			Name:      "second.txt",
			Size:      int64(len(secondText)),
			SHA256:    bytesSHA256(secondText),
			ChunkSize: protocol.ChunkSize,
		},
	})
	manifest.Streams = []protocol.StreamBinding{
		{ID: 9, Item: 0},
		{ID: 20, Item: 1},
		{ID: 3, Item: 2},
	}

	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	resume := store.ResumeEntries()
	if len(resume) != 1 || resume[0].Item != 1 || resume[0].Stream == nil || *resume[0].Stream != 20 || resume[0].Offset != 0 {
		t.Fatalf("unexpected resume entries: %#v", resume)
	}

	if err := store.WriteChunk(1, 0, fileData[:4]); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, 0, firstText); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(2, 0, secondText); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(1, 4, fileData[4:]); err != nil {
		t.Fatal(err)
	}

	texts, err := store.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 || texts[0].Name != "first.txt" || texts[0].Text != "first" || texts[1].Name != "second.txt" || texts[1].Text != "second" {
		t.Fatalf("unexpected texts: %#v", texts)
	}
	got, err := os.ReadFile(filepath.Join(outputDir, "bundle", "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(fileData) {
		t.Fatalf("saved file = %q, want %q", got, fileData)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "bundle", "data.bin.kigopart")); !os.IsNotExist(err) {
		t.Fatalf("part file still exists after finalize: %v", err)
	}
}

func TestReceiveStoreResumesExistingPart(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("prefix-and-suffix")
	partPath := filepath.Join(outputDir, "data.bin.kigopart")
	if err := os.WriteFile(partPath, data[:7], 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            "data.bin",
		Size:            int64(len(data)),
		SHA256:          bytesSHA256(data),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}})

	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	resume := store.ResumeEntries()
	if len(resume) != 1 || resume[0].Offset != 7 {
		t.Fatalf("unexpected resume entries: %#v", resume)
	}
	if resume[0].PrefixSHA256 != bytesSHA256(data[:7]) {
		t.Fatalf("prefix sha256 = %q", resume[0].PrefixSHA256)
	}
	if err := store.WriteChunk(0, 7, data[7:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outputDir, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("saved file = %q, want %q", got, data)
	}
}

func TestReceiveStoreAppliesRejectedResumeOffset(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("prefix-and-suffix")
	partPath := filepath.Join(outputDir, "data.bin.kigopart")
	if err := os.WriteFile(partPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            "data.bin",
		Size:            int64(len(data)),
		SHA256:          bytesSHA256(data),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}})
	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stream := 0
	if err := store.ApplyResumeAccept([]protocol.ResumeEntry{{Item: 0, Stream: &stream, Offset: 0}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(partPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("part size = %d", info.Size())
	}
	if err := store.WriteChunk(0, 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
}

func TestReceiveStoreWritesFileChunksOutOfOrder(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("prefix-middle-suffix")
	manifest := fileManifest("striped.bin", data)
	store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir:  outputDir,
		OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.WriteChunk(0, 13, data[13:]); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, 0, data[:7]); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, 7, data[7:13]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outputDir, "striped.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("saved file = %q, want %q", got, data)
	}
}

func TestReceiveStoreTruncatesOutOfOrderTailOnClose(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("0123456789")
	manifest := fileManifest("striped.bin", data)
	store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir:  outputDir,
		OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, 0, data[:3]); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, 6, data[6:]); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(outputDir, "striped.bin.kigopart"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 3 {
		t.Fatalf("part size = %d, want contiguous prefix 3", info.Size())
	}
}

func TestReceiveStoreRejectsOverlappingOutOfOrderChunk(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("0123456789")
	manifest := fileManifest("striped.bin", data)
	store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir:  outputDir,
		OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.WriteChunk(0, 4, data[4:8]); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, 6, data[6:]); err == nil {
		t.Fatal("overlapping out-of-order chunk was accepted")
	}
}

func TestReceiveStoreSkipsMatchingCompletedFile(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("already complete")
	finalPath := filepath.Join(outputDir, "data.bin")
	if err := os.WriteFile(finalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sampleHash, err := fileSampleSHA256(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	wantTime := time.Unix(1_700_000_123, 0)
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            "data.bin",
		Size:            int64(len(data)),
		SHA256:          bytesSHA256([]byte("different declared full hash")),
		SampleSHA256:    sampleHash,
		MTime:           wantTime.UnixMilli(),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}})

	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resume := store.ResumeEntries()
	if len(resume) != 1 || resume[0].Offset != int64(len(data)) || !resume[0].Skip || !resume[0].Complete || resume[0].PrefixSHA256 != "" {
		t.Fatalf("resume entries = %#v", resume)
	}
	if err := store.ApplyResumeAccept(resume); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("completed file changed: %q", got)
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != wantTime.Unix() {
		t.Fatalf("completed file mtime=%s want=%s", info.ModTime(), wantTime)
	}
}

func TestReceiveStoreDoesNotFastSkipDifferentSample(t *testing.T) {
	outputDir := t.TempDir()
	finalPath := filepath.Join(outputDir, "data.bin")
	if err := os.WriteFile(finalPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	incoming := []byte("incoming")
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            "data.bin",
		Size:            int64(len(incoming)),
		SHA256:          bytesSHA256(incoming),
		SampleSHA256:    bytesSHA256(incoming),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}})
	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resume := store.ResumeEntries()
	if len(resume) != 1 || resume[0].Offset != 0 || resume[0].Skip || resume[0].Complete {
		t.Fatalf("different sample was fast-skipped: %#v", resume)
	}
}

func TestReceiveStoreKeepsPartOnIntegrityFailure(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("corrupt")
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            "data.bin",
		Size:            int64(len(data)),
		SHA256:          bytesSHA256([]byte("correct")),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}})

	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.WriteChunk(0, 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err == nil {
		t.Fatal("integrity failure was accepted")
	}
	if _, err := os.Stat(filepath.Join(outputDir, "data.bin")); !os.IsNotExist(err) {
		t.Fatalf("final file exists after integrity failure: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(outputDir, "data.bin.kigopart")); err != nil || string(got) != string(data) {
		t.Fatalf("part file was not preserved: data=%q err=%v", got, err)
	}
}

func TestReceiveStoreSkipsDifferentExistingFile(t *testing.T) {
	outputDir := t.TempDir()
	finalPath := filepath.Join(outputDir, "data.bin")
	if err := os.WriteFile(finalPath, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("new payload")
	manifest := fileManifest("data.bin", data)
	store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir: outputDir,
		Conflict:  ConflictSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	resume := store.ResumeEntries()
	if len(resume) != 1 || !resume[0].Skip || resume[0].Offset != int64(len(data)) {
		t.Fatalf("resume entries = %#v", resume)
	}
	if err := store.ApplyResumeAccept(resume); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteChunk(0, int64(len(data)), []byte("x")); err == nil {
		t.Fatal("chunk for skipped file was accepted")
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me" {
		t.Fatalf("existing file changed: %q", got)
	}
	if _, err := os.Stat(finalPath + ".kigopart"); !os.IsNotExist(err) {
		t.Fatalf("part file created for skipped destination: %v", err)
	}
}

func TestReceiveStoreRenameReusesPart(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "data.bin"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("prefix-and-suffix")
	partPath := filepath.Join(outputDir, "data (1).bin.kigopart")
	if err := os.WriteFile(partPath, data[:7], 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fileManifest("data.bin", data)
	store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir: outputDir,
		Conflict:  ConflictRename,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	resume := store.ResumeEntries()
	if len(resume) != 1 || resume[0].Offset != 7 {
		t.Fatalf("resume entries = %#v", resume)
	}
	if err := store.WriteChunk(0, 7, data[7:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outputDir, "data (1).bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("renamed file = %q, want %q", got, data)
	}
}

func TestReceiveStoreOverwriteReplacesDifferentFile(t *testing.T) {
	outputDir := t.TempDir()
	finalPath := filepath.Join(outputDir, "data.bin")
	if err := os.WriteFile(finalPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("new")
	manifest := fileManifest("data.bin", data)
	store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir: outputDir,
		Conflict:  ConflictOverwrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.WriteChunk(0, 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("overwritten file = %q, want %q", got, data)
	}
}

func TestReceiveStoreRestoresModeAndMTime(t *testing.T) {
	outputDir := t.TempDir()
	data := []byte("metadata")
	manifest := fileManifest("data.bin", data)
	wantTime := time.Unix(1_700_000_000, 123_000_000)
	manifest.Items[0].Mode = 0o640
	manifest.Items[0].MTime = wantTime.UnixMilli()
	store, err := NewReceiveStore(&manifest, outputDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.WriteChunk(0, 0, data); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(outputDir, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
	if info.ModTime().UnixMilli() != wantTime.UnixMilli() {
		t.Fatalf("mtime = %s, want %s", info.ModTime(), wantTime)
	}
}

func TestReceiveStoreOverwriteRefusesDirectory(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(outputDir, "data.bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fileManifest("data.bin", []byte("payload"))
	if _, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
		OutputDir: outputDir,
		Conflict:  ConflictOverwrite,
	}); err == nil {
		t.Fatal("directory destination was accepted for overwrite")
	}
}

func TestParseConflictPolicy(t *testing.T) {
	for _, value := range []string{"", "overwrite", "skip", "rename", " RENAME "} {
		if _, err := ParseConflictPolicy(value); err != nil {
			t.Fatalf("ParseConflictPolicy(%q): %v", value, err)
		}
	}
	if _, err := ParseConflictPolicy("replace"); err == nil {
		t.Fatal("invalid conflict policy was accepted")
	}
}

func TestReceiveStoreSymlinkConflictPolicies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation may require privileges")
	}
	for _, tc := range []struct {
		name       string
		policy     ConflictPolicy
		wantPath   string
		wantTarget string
		wantData   string
	}{
		{name: "overwrite", policy: ConflictOverwrite, wantPath: "link", wantTarget: "target.txt"},
		{name: "skip", policy: ConflictSkip, wantPath: "link", wantData: "existing"},
		{name: "rename", policy: ConflictRename, wantPath: "link (1)", wantTarget: "target.txt", wantData: "existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(outputDir, "link"), []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := symlinkManifest("link", "target.txt")
			store, err := NewReceiveStoreWithOptions(&manifest, ReceiveStoreOptions{
				OutputDir: outputDir,
				Conflict:  tc.policy,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err := store.Finalize(); err != nil {
				t.Fatal(err)
			}
			resultPath := filepath.Join(outputDir, tc.wantPath)
			if tc.wantTarget != "" {
				target, err := os.Readlink(resultPath)
				if err != nil {
					t.Fatal(err)
				}
				if target != tc.wantTarget {
					t.Fatalf("target = %q, want %q", target, tc.wantTarget)
				}
			}
			if tc.wantData != "" {
				got, err := os.ReadFile(filepath.Join(outputDir, "link"))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.wantData {
					t.Fatalf("existing data = %q", got)
				}
			}
		})
	}
}

func TestReceiveStoreRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation may require privileges")
	}
	outputDir := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(outputDir, "bundle")); err != nil {
		t.Fatal(err)
	}
	manifest := fileManifest("bundle/data.bin", []byte("payload"))
	if _, err := NewReceiveStore(&manifest, outputDir, nil); err == nil {
		t.Fatal("symlink parent was accepted")
	}
}

func TestReceiveStoreRejectsUnsafeSymlinkManifest(t *testing.T) {
	manifest := symlinkManifest("link", "../outside")
	if _, err := NewReceiveStore(&manifest, t.TempDir(), nil); err == nil {
		t.Fatal("unsafe symlink target was accepted")
	}
}

func TestReceiveStoreRejectsNonDirectoryManifestParent(t *testing.T) {
	manifest := protocol.NewManifest([]protocol.Item{
		{
			Kind:      protocol.ItemSymlink,
			Name:      "bundle",
			Target:    "target",
			ChunkSize: protocol.ChunkSize,
		},
		{
			Kind:            protocol.ItemFile,
			Name:            "bundle/data.bin",
			Size:            1,
			SHA256:          bytesSHA256([]byte("x")),
			ChunkSize:       protocol.ChunkSize,
			ResumeSupported: true,
		},
	})
	if _, err := NewReceiveStore(&manifest, t.TempDir(), nil); err == nil {
		t.Fatal("manifest with symlink parent was accepted")
	}
}

func fileManifest(name string, data []byte) protocol.Manifest {
	return protocol.NewManifest([]protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            name,
		Size:            int64(len(data)),
		SHA256:          bytesSHA256(data),
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}})
}

func symlinkManifest(name, target string) protocol.Manifest {
	return protocol.NewManifest([]protocol.Item{{
		Kind:      protocol.ItemSymlink,
		Name:      name,
		Target:    target,
		ChunkSize: protocol.ChunkSize,
	}})
}
