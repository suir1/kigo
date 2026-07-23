package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/mux"
	"github.com/suir1/kigo/internal/protocol"
)

type ReceiveStore struct {
	manifest   *protocol.Manifest
	plan       *mux.Plan
	outputDir  string
	logf       Logger
	files      map[int]*receiveFile
	texts      map[int]*receiveText
	dirs       map[int]*receiveDirectory
	symlinks   map[int]*receiveSymlink
	resume     []protocol.ResumeEntry
	conflict   ConflictPolicy
	claimed    map[string]bool
	reserved   map[string]bool
	outOfOrder bool
}

type receiveFile struct {
	file      *os.File
	finalPath string
	partPath  string
	offset    int64
	complete  bool
	skipped   bool
	overwrite bool
	ranges    *byteRanges
}

type receiveText struct {
	builder strings.Builder
	offset  int64
}

type receiveDirectory struct {
	finalPath string
}

type receiveSymlink struct {
	finalPath string
	target    string
	skipped   bool
	overwrite bool
}

type ConflictPolicy string

const (
	ConflictOverwrite ConflictPolicy = "overwrite"
	ConflictSkip      ConflictPolicy = "skip"
	ConflictRename    ConflictPolicy = "rename"
)

type ReceiveStoreOptions struct {
	OutputDir  string
	Logf       Logger
	Conflict   ConflictPolicy
	OutOfOrder bool
}

func ParseConflictPolicy(value string) (ConflictPolicy, error) {
	switch policy := ConflictPolicy(strings.ToLower(strings.TrimSpace(value))); policy {
	case "", ConflictOverwrite:
		return ConflictOverwrite, nil
	case ConflictSkip, ConflictRename:
		return policy, nil
	default:
		return "", fmt.Errorf("invalid conflict policy %q; want overwrite, skip, or rename", value)
	}
}

func NewReceiveStore(manifest *protocol.Manifest, outputDir string, logf Logger) (*ReceiveStore, error) {
	return NewReceiveStoreWithOptions(manifest, ReceiveStoreOptions{
		OutputDir: outputDir,
		Logf:      logf,
		Conflict:  ConflictOverwrite,
	})
}

func NewReceiveStoreWithOptions(manifest *protocol.Manifest, opts ReceiveStoreOptions) (*ReceiveStore, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	conflict, err := ParseConflictPolicy(string(opts.Conflict))
	if err != nil {
		return nil, err
	}
	plan, err := mux.PlanFromManifest(manifest)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, err
	}
	store := &ReceiveStore{
		manifest:   manifest,
		plan:       plan,
		outputDir:  opts.OutputDir,
		logf:       opts.Logf,
		files:      map[int]*receiveFile{},
		texts:      map[int]*receiveText{},
		dirs:       map[int]*receiveDirectory{},
		symlinks:   map[int]*receiveSymlink{},
		conflict:   conflict,
		claimed:    map[string]bool{},
		reserved:   map[string]bool{},
		outOfOrder: opts.OutOfOrder,
	}
	for _, item := range manifest.Items {
		switch item.Kind {
		case protocol.ItemFile, protocol.ItemDirectory, protocol.ItemSymlink:
			name, err := safeRelativePath(item.Name)
			if err != nil {
				return nil, err
			}
			store.reserved[filepath.Join(opts.OutputDir, name)] = true
		}
	}
	for i, item := range manifest.Items {
		switch item.Kind {
		case protocol.ItemFile:
			if err := store.openFile(i, item); err != nil {
				_ = store.Close()
				return nil, err
			}
		case protocol.ItemText:
			store.texts[i] = &receiveText{}
		case protocol.ItemDirectory:
			if err := store.openDirectory(i, item); err != nil {
				_ = store.Close()
				return nil, err
			}
		case protocol.ItemSymlink:
			if err := store.planSymlink(i, item); err != nil {
				_ = store.Close()
				return nil, err
			}
		}
	}
	return store, nil
}

func (s *ReceiveStore) openFile(itemID int, item protocol.Item) error {
	name, err := safeRelativePath(item.Name)
	if err != nil {
		return err
	}
	finalPath := filepath.Join(s.outputDir, name)
	if err := ensureSafeParent(s.outputDir, finalPath); err != nil {
		return err
	}
	streamID, ok := s.plan.StreamForItem(itemID)
	if !ok {
		return fmt.Errorf("manifest item %d has no stream binding", itemID)
	}
	if completedFileMatches(finalPath, item) {
		s.claimed[finalPath] = true
		s.files[itemID] = &receiveFile{finalPath: finalPath, offset: item.Size, complete: true, ranges: newByteRanges(item.Size)}
		s.resume = append(s.resume, protocol.ResumeEntry{
			Item:     itemID,
			Stream:   ptr(streamID),
			Offset:   item.Size,
			Skip:     true,
			Complete: true,
		})
		if s.logf != nil {
			s.logf("already complete %s (%d bytes)", item.Name, item.Size)
		}
		return nil
	}

	exists, regular, err := pathState(finalPath)
	if err != nil {
		return err
	}
	if s.claimed[finalPath] {
		exists = true
		regular = true
	}
	switch {
	case exists && s.conflict == ConflictSkip:
		s.claimed[finalPath] = true
		s.files[itemID] = &receiveFile{
			finalPath: finalPath,
			offset:    item.Size,
			skipped:   true,
			ranges:    newByteRanges(item.Size),
		}
		s.resume = append(s.resume, protocol.ResumeEntry{
			Item:   itemID,
			Stream: ptr(streamID),
			Offset: item.Size,
			Skip:   true,
		})
		if s.logf != nil {
			s.logf("skipping existing %s", finalPath)
		}
		return nil
	case exists && s.conflict == ConflictRename:
		finalPath, err = s.availableRenamePath(finalPath)
		if err != nil {
			return err
		}
	case exists && s.conflict == ConflictOverwrite && !regular:
		return fmt.Errorf("refusing to overwrite non-regular path %s", finalPath)
	}

	s.claimed[finalPath] = true
	partPath := finalPath + ".kigopart"
	partExists, partRegular, err := pathState(partPath)
	if err != nil {
		return err
	}
	if partExists && !partRegular {
		return fmt.Errorf("refusing to use non-regular part path %s", partPath)
	}
	resumeOffset := resumableOffset(partPath, item)
	file, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Truncate(resumeOffset); err != nil {
		_ = file.Close()
		return err
	}
	s.files[itemID] = &receiveFile{
		file:      file,
		finalPath: finalPath,
		partPath:  partPath,
		offset:    resumeOffset,
		overwrite: exists && s.conflict == ConflictOverwrite,
		ranges:    newByteRanges(resumeOffset),
	}
	resumeEntry := protocol.ResumeEntry{Item: itemID, Stream: ptr(streamID), Offset: resumeOffset}
	if resumeOffset > 0 {
		prefixHash, err := fileSHA256(partPath)
		if err != nil {
			_ = file.Close()
			return err
		}
		resumeEntry.PrefixSHA256 = prefixHash
	}
	s.resume = append(s.resume, resumeEntry)
	if resumeOffset > 0 && s.logf != nil {
		s.logf("resuming %s from %d/%d bytes", item.Name, resumeOffset, item.Size)
	}
	return nil
}

func (s *ReceiveStore) openDirectory(itemID int, item protocol.Item) error {
	name, err := safeRelativePath(item.Name)
	if err != nil {
		return err
	}
	finalPath := filepath.Join(s.outputDir, name)
	if err := ensureSafeParent(s.outputDir, finalPath); err != nil {
		return err
	}
	info, err := os.Lstat(finalPath)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("directory target is not a directory: %s", finalPath)
	case err != nil && !os.IsNotExist(err):
		return err
	case os.IsNotExist(err):
		if err := os.Mkdir(finalPath, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(finalPath)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("directory target is not a directory: %s", finalPath)
		}
	}
	s.claimed[finalPath] = true
	s.dirs[itemID] = &receiveDirectory{finalPath: finalPath}
	return nil
}

func (s *ReceiveStore) planSymlink(itemID int, item protocol.Item) error {
	name, err := safeRelativePath(item.Name)
	if err != nil {
		return err
	}
	if err := validateSafeSymlinkTarget(item.Name, item.Target); err != nil {
		return err
	}
	finalPath := filepath.Join(s.outputDir, name)
	if err := ensureSafeParent(s.outputDir, finalPath); err != nil {
		return err
	}
	if symlinkMatches(finalPath, item.Target) {
		s.claimed[finalPath] = true
		s.symlinks[itemID] = &receiveSymlink{finalPath: finalPath, target: item.Target, skipped: true}
		if s.logf != nil {
			s.logf("already linked %s", item.Name)
		}
		return nil
	}
	exists, _, err := pathState(finalPath)
	if err != nil {
		return err
	}
	if s.claimed[finalPath] {
		exists = true
	}
	state := &receiveSymlink{finalPath: finalPath, target: item.Target}
	switch {
	case exists && s.conflict == ConflictSkip:
		state.skipped = true
		if s.logf != nil {
			s.logf("skipping existing %s", finalPath)
		}
	case exists && s.conflict == ConflictRename:
		state.finalPath, err = s.availableRenamePath(finalPath)
		if err != nil {
			return err
		}
	case exists && s.conflict == ConflictOverwrite:
		info, err := os.Lstat(finalPath)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("refusing to overwrite directory with symlink %s", finalPath)
		}
		state.overwrite = true
	}
	s.claimed[state.finalPath] = true
	s.symlinks[itemID] = state
	return nil
}

func (s *ReceiveStore) availableRenamePath(path string) (string, error) {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if s.claimed[candidate] || s.reserved[candidate] {
			continue
		}
		exists, _, err := pathState(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
}

func ensureSafeParent(root, path string) error {
	parent := filepath.Dir(path)
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes output directory: %s", path)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("receive parent is not a directory: %s", current)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("receive parent is not a directory: %s", current)
		}
	}
	return nil
}

func symlinkMatches(path, target string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	got, err := os.Readlink(path)
	return err == nil && filepath.ToSlash(got) == filepath.ToSlash(target)
}

func pathState(path string) (exists bool, regular bool, err error) {
	info, err := os.Lstat(path)
	if err == nil {
		return true, info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, false, nil
	}
	return false, false, err
}

func (s *ReceiveStore) ResumeEntries() []protocol.ResumeEntry {
	return append([]protocol.ResumeEntry(nil), s.resume...)
}

func (s *ReceiveStore) ApplyResumeAccept(entries []protocol.ResumeEntry) error {
	if s == nil || s.manifest == nil || s.plan == nil {
		return errors.New("receive store is not initialized")
	}
	accepted := make(map[int]protocol.ResumeEntry, len(entries))
	for _, entry := range entries {
		offset, err := s.plan.ValidateResumeEntry(s.manifest.Items, entry)
		if err != nil {
			return err
		}
		if _, ok := accepted[entry.Item]; ok {
			return fmt.Errorf("duplicate resume_accept entry for item %d", entry.Item)
		}
		entry.Offset = offset
		if entry.SHA256 != "" {
			declared := strings.ToLower(s.manifest.Items[entry.Item].SHA256)
			acceptedSHA256 := strings.ToLower(entry.SHA256)
			if declared != "" && declared != acceptedSHA256 {
				return fmt.Errorf("resume_accept sha256 for %s conflicts with manifest", s.manifest.Items[entry.Item].Name)
			}
			s.manifest.Items[entry.Item].SHA256 = acceptedSHA256
		}
		accepted[entry.Item] = entry
	}
	for itemID, state := range s.files {
		entry, ok := accepted[itemID]
		if !ok {
			return fmt.Errorf("resume_accept missing file item %d", itemID)
		}
		if entry.Offset > state.offset {
			return fmt.Errorf("resume_accept offset for %s exceeds requested offset: %d > %d", s.manifest.Items[itemID].Name, entry.Offset, state.offset)
		}
		if state.skipped {
			if !entry.Skip || entry.Offset != state.offset {
				return fmt.Errorf("sender rejected skip for existing file %s", s.manifest.Items[itemID].Name)
			}
			continue
		}
		if state.complete {
			if !entry.Skip || entry.Offset != state.offset {
				return fmt.Errorf("sender rejected verified completed file %s", s.manifest.Items[itemID].Name)
			}
			continue
		}
		if entry.Skip {
			return fmt.Errorf("sender unexpectedly skipped file %s", s.manifest.Items[itemID].Name)
		}
		if state.file == nil {
			return errors.New("file output is not open")
		}
		if entry.Offset != state.offset {
			if err := state.file.Truncate(entry.Offset); err != nil {
				return err
			}
			state.offset = entry.Offset
			state.ranges = newByteRanges(entry.Offset)
			if s.logf != nil {
				s.logf("sender accepted %s resume at %d bytes", s.manifest.Items[itemID].Name, entry.Offset)
			}
		}
	}
	s.resume = append([]protocol.ResumeEntry(nil), entries...)
	return nil
}

func (s *ReceiveStore) WriteChunk(itemID int, offset int64, data []byte) error {
	if s == nil || s.manifest == nil || itemID < 0 || itemID >= len(s.manifest.Items) {
		return fmt.Errorf("chunk item index out of range: %d", itemID)
	}
	item := s.manifest.Items[itemID]
	switch item.Kind {
	case protocol.ItemFile:
		state := s.files[itemID]
		if state == nil {
			return errors.New("file output is not open")
		}
		if state.complete || state.skipped {
			return fmt.Errorf("received chunk for completed or skipped file %s", item.Name)
		}
		if state.file == nil {
			return errors.New("file output is not open")
		}
		if err := validateChunkRange(item, offset, len(data)); err != nil {
			return err
		}
		if !s.outOfOrder && offset != state.offset {
			return fmt.Errorf("unexpected chunk offset for %s: got %d want %d", item.Name, offset, state.offset)
		}
		nextRanges := &byteRanges{ranges: append([]byteRange(nil), state.ranges.ranges...)}
		if err := nextRanges.Add(offset, offset+int64(len(data))); err != nil {
			return fmt.Errorf("invalid chunk range for %s: %w", item.Name, err)
		}
		if _, err := state.file.WriteAt(data, offset); err != nil {
			return err
		}
		state.ranges = nextRanges
		state.offset = state.ranges.Prefix()
	case protocol.ItemText:
		state := s.texts[itemID]
		if state == nil {
			return errors.New("text output is not open")
		}
		if err := validateChunkBounds(item, offset, len(data), state.offset); err != nil {
			return err
		}
		_, _ = state.builder.Write(data)
		state.offset += int64(len(data))
	default:
		return fmt.Errorf("unsupported item kind %q", item.Kind)
	}
	return nil
}

func (s *ReceiveStore) Finalize() ([]ReceivedText, error) {
	if s == nil || s.manifest == nil {
		return nil, errors.New("receive store is not initialized")
	}
	texts := make([]ReceivedText, 0, len(s.texts))
	for i, item := range s.manifest.Items {
		switch item.Kind {
		case protocol.ItemFile:
			state := s.files[i]
			if state == nil {
				return nil, errors.New("file output is not open")
			}
			if state.skipped {
				if s.logf != nil {
					s.logf("left existing %s unchanged", state.finalPath)
				}
				continue
			}
			if state.complete {
				if err := applyPathMetadata(state.finalPath, item); err != nil {
					return nil, err
				}
				if s.logf != nil {
					s.logf("kept existing %s", state.finalPath)
				}
				continue
			}
			if state.file == nil {
				return nil, errors.New("file output is not open")
			}
			if state.ranges == nil || !state.ranges.Complete(item.Size) {
				return nil, fmt.Errorf("received size mismatch for %s: contiguous prefix %d want %d", item.Name, state.offset, item.Size)
			}
			if err := state.file.Close(); err != nil {
				return nil, err
			}
			state.file = nil
			if err := verifyPartFile(state.partPath, item); err != nil {
				return nil, err
			}
			if err := finalizeReceivedFile(state, item); err != nil {
				return nil, err
			}
			if s.logf != nil {
				s.logf("saved %s", state.finalPath)
			}
		case protocol.ItemText:
			state := s.texts[i]
			if state == nil {
				return nil, errors.New("text output is not open")
			}
			text := state.builder.String()
			if err := verifyTextItem(text, item); err != nil {
				return nil, err
			}
			texts = append(texts, ReceivedText{Name: item.Name, Text: text})
		case protocol.ItemDirectory, protocol.ItemSymlink:
		}
	}
	for i, item := range s.manifest.Items {
		if item.Kind != protocol.ItemSymlink {
			continue
		}
		state := s.symlinks[i]
		if state == nil {
			return nil, errors.New("symlink output is not planned")
		}
		if state.skipped {
			continue
		}
		if err := finalizeSymlink(state); err != nil {
			return nil, err
		}
		if s.logf != nil {
			s.logf("linked %s -> %s", state.finalPath, state.target)
		}
	}
	directoryItems := make([]int, 0, len(s.dirs))
	for itemID := range s.dirs {
		directoryItems = append(directoryItems, itemID)
	}
	sort.Slice(directoryItems, func(i, j int) bool {
		return pathDepth(s.dirs[directoryItems[i]].finalPath) > pathDepth(s.dirs[directoryItems[j]].finalPath)
	})
	for _, itemID := range directoryItems {
		state := s.dirs[itemID]
		if err := applyPathMetadata(state.finalPath, s.manifest.Items[itemID]); err != nil {
			return nil, err
		}
	}
	return texts, nil
}

func finalizeSymlink(state *receiveSymlink) error {
	if state.overwrite {
		info, err := os.Lstat(state.finalPath)
		if err == nil {
			if info.IsDir() {
				return fmt.Errorf("refusing to overwrite directory with symlink %s", state.finalPath)
			}
			if err := os.Remove(state.finalPath); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	} else if exists, _, err := pathState(state.finalPath); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("destination appeared during transfer: %s", state.finalPath)
	}
	return os.Symlink(filepath.FromSlash(state.target), state.finalPath)
}

func pathDepth(path string) int {
	return len(strings.Split(filepath.Clean(path), string(filepath.Separator)))
}

func applyPathMetadata(path string, item protocol.Item) error {
	if item.Mode != 0 {
		if err := os.Chmod(path, os.FileMode(item.Mode)&os.ModePerm); err != nil {
			return err
		}
	}
	if item.MTime != 0 {
		mtime := time.UnixMilli(item.MTime)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			return err
		}
	}
	return nil
}

func finalizeReceivedFile(state *receiveFile, item protocol.Item) error {
	if err := applyPathMetadata(state.partPath, item); err != nil {
		return err
	}
	exists, regular, err := pathState(state.finalPath)
	if err != nil {
		return err
	}
	if exists {
		if !state.overwrite {
			return fmt.Errorf("destination appeared during transfer: %s", state.finalPath)
		}
		if !regular {
			return fmt.Errorf("refusing to overwrite non-regular path %s", state.finalPath)
		}
	}
	if runtime.GOOS == "windows" && exists {
		if err := os.Remove(state.finalPath); err != nil {
			return err
		}
	}
	if err := os.Rename(state.partPath, state.finalPath); err != nil {
		return err
	}
	return nil
}

func (s *ReceiveStore) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	for _, state := range s.files {
		if state == nil || state.file == nil {
			continue
		}
		if s.outOfOrder {
			if err := state.file.Truncate(state.offset); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := state.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		state.file = nil
	}
	return firstErr
}

func resumableOffset(partPath string, item protocol.Item) int64 {
	if !item.ResumeSupported {
		return 0
	}
	info, err := os.Stat(partPath)
	if err != nil || info.IsDir() {
		return 0
	}
	return clampInt64(info.Size(), 0, item.Size)
}

func completedFileMatches(finalPath string, item protocol.Item) bool {
	if item.Kind != protocol.ItemFile {
		return false
	}
	info, err := os.Lstat(finalPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
		return false
	}
	if item.SampleSHA256 != "" {
		hash, err := fileSampleSHA256(finalPath)
		return err == nil && hash == strings.ToLower(item.SampleSHA256)
	}
	if item.SHA256 == "" {
		return true
	}
	hash, err := fileSHA256(finalPath)
	return err == nil && hash == strings.ToLower(item.SHA256)
}

func validateManifest(manifest *protocol.Manifest) error {
	if manifest == nil {
		return errors.New("manifest message missing manifest")
	}
	if manifest.Version != protocol.Version {
		return fmt.Errorf("unsupported manifest version %d", manifest.Version)
	}
	if len(manifest.Items) == 0 {
		return errors.New("manifest has no items")
	}
	paths := map[string]protocol.ItemKind{}
	for i, item := range manifest.Items {
		if item.Name == "" {
			return fmt.Errorf("manifest item %d has empty name", i)
		}
		if item.Size < 0 {
			return fmt.Errorf("manifest item %s has negative size %d", item.Name, item.Size)
		}
		if item.ChunkSize <= 0 || item.ChunkSize > protocol.ChunkSize {
			return fmt.Errorf("manifest item %s has invalid chunk size %d", item.Name, item.ChunkSize)
		}
		if item.SHA256 != "" && !isHexSHA256(item.SHA256) {
			return fmt.Errorf("manifest item %s has invalid sha256", item.Name)
		}
		if item.SampleSHA256 != "" && !isHexSHA256(item.SampleSHA256) {
			return fmt.Errorf("manifest item %s has invalid sample_sha256", item.Name)
		}
		hasPath := false
		switch item.Kind {
		case protocol.ItemFile:
			if item.Target != "" {
				return fmt.Errorf("manifest file %s has symlink target", item.Name)
			}
			hasPath = true
		case protocol.ItemText:
			if item.SampleSHA256 != "" {
				return fmt.Errorf("manifest text %s has sample hash", item.Name)
			}
		case protocol.ItemDirectory:
			if item.Size != 0 || item.SHA256 != "" || item.SampleSHA256 != "" || item.Target != "" || item.ResumeSupported {
				return fmt.Errorf("manifest directory %s has file data fields", item.Name)
			}
			hasPath = true
		case protocol.ItemSymlink:
			if item.Size != 0 || item.SHA256 != "" || item.SampleSHA256 != "" || item.ResumeSupported {
				return fmt.Errorf("manifest symlink %s has file data fields", item.Name)
			}
			if err := validateSafeSymlinkTarget(item.Name, item.Target); err != nil {
				return err
			}
			hasPath = true
		default:
			return fmt.Errorf("unsupported item kind %q", item.Kind)
		}
		if hasPath {
			name, err := safeRelativePath(item.Name)
			if err != nil {
				return err
			}
			key := filepath.ToSlash(name)
			if _, exists := paths[key]; exists {
				return fmt.Errorf("duplicate path in manifest: %s", item.Name)
			}
			paths[key] = item.Kind
		}
	}
	for key := range paths {
		parts := strings.Split(key, "/")
		for n := 1; n < len(parts); n++ {
			parent := strings.Join(parts[:n], "/")
			if kind, exists := paths[parent]; exists && kind != protocol.ItemDirectory {
				return fmt.Errorf("manifest path %s has non-directory parent %s", key, parent)
			}
		}
	}
	return nil
}

func validateSafeSymlinkTarget(name, target string) error {
	if target == "" || strings.IndexByte(target, 0) >= 0 {
		return fmt.Errorf("refusing empty symlink target for %s", name)
	}
	normalized := strings.ReplaceAll(target, "\\", "/")
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(filepath.FromSlash(normalized)) {
		return fmt.Errorf("refusing absolute symlink target for %s: %s", name, target)
	}
	if len(normalized) >= 2 && normalized[1] == ':' {
		return fmt.Errorf("refusing drive-qualified symlink target for %s: %s", name, target)
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return fmt.Errorf("refusing unsafe symlink target for %s: %s", name, target)
		}
	}
	return nil
}

func isHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func verifyTextItem(text string, item protocol.Item) error {
	data := []byte(text)
	if int64(len(data)) != item.Size {
		return fmt.Errorf("received size mismatch for %s: got %d want %d", item.Name, len(data), item.Size)
	}
	if item.SHA256 == "" {
		return nil
	}
	if got := bytesSHA256(data); got != strings.ToLower(item.SHA256) {
		return fmt.Errorf("sha256 mismatch for %s", item.Name)
	}
	return nil
}

func validateChunkBounds(item protocol.Item, offset int64, dataLen int, expectedOffset int64) error {
	if err := validateChunkRange(item, offset, dataLen); err != nil {
		return err
	}
	if offset != expectedOffset {
		return fmt.Errorf("unexpected chunk offset for %s: got %d want %d", item.Name, offset, expectedOffset)
	}
	return nil
}

func validateChunkRange(item protocol.Item, offset int64, dataLen int) error {
	if offset < 0 {
		return fmt.Errorf("negative chunk offset for %s: %d", item.Name, offset)
	}
	end := offset + int64(dataLen)
	if end < offset || end > item.Size {
		return fmt.Errorf("chunk exceeds declared size for %s: end %d size %d", item.Name, end, item.Size)
	}
	return nil
}

func verifyPartFile(partPath string, item protocol.Item) error {
	info, err := os.Stat(partPath)
	if err != nil {
		return err
	}
	if info.Size() != item.Size {
		return fmt.Errorf("received size mismatch for %s: got %d want %d", item.Name, info.Size(), item.Size)
	}
	if item.SHA256 == "" {
		return nil
	}
	got, err := fileSHA256(partPath)
	if err != nil {
		return err
	}
	if got != strings.ToLower(item.SHA256) {
		return fmt.Errorf("sha256 mismatch for %s", item.Name)
	}
	return nil
}

func safeRelativePath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	clean := filepath.Clean(name)
	if clean == "." || clean == string(filepath.Separator) || clean == "" {
		return "", errors.New("empty file path in manifest")
	}
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("absolute file path in manifest: %s", name)
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unsafe file path in manifest: %s", name)
		}
	}
	return filepath.FromSlash(strings.Join(parts, "/")), nil
}
