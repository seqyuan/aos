package tosx

import (
	"os"
	"path/filepath"
)

func writeTestFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func makeSymlink(target, link string) error {
	return os.Symlink(target, link)
}
