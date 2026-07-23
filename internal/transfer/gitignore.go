package transfer

import (
	"bufio"
	"os"
	"path/filepath"

	ignore "github.com/sabhiram/go-gitignore"
)

type gitIgnoreMatcher struct {
	rules *ignore.GitIgnore
}

func loadGitIgnoreStack(root string) (*gitIgnoreMatcher, error) {
	var lines []string
	current, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(current, ".gitignore")
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			scanErr := scanner.Err()
			closeErr := file.Close()
			if scanErr != nil {
				return nil, scanErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		case statErr != nil && !os.IsNotExist(statErr):
			return nil, statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return &gitIgnoreMatcher{rules: ignore.CompileIgnoreLines(lines...)}, nil
}

func (m *gitIgnoreMatcher) Ignored(relative string, directory bool) bool {
	if m == nil || m.rules == nil || relative == "." || relative == "" {
		return false
	}
	path := filepath.ToSlash(relative)
	if directory {
		path += "/"
	}
	return m.rules.MatchesPath(path)
}
