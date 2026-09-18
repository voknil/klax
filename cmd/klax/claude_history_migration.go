package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func migrateClaudeTranscript(oldCWD, newCWD, sessionID string) error {
	if sessionID == "" || oldCWD == "" || newCWD == "" || oldCWD == newCWD {
		return nil
	}
	base, err := claudeProjectsDir()
	if err != nil {
		return err
	}
	srcFile := filepath.Join(claudeProjectDir(base, oldCWD), sessionID+".jsonl")
	if _, err := os.Stat(srcFile); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		found, findErr := findClaudeTranscript(base, sessionID)
		if findErr != nil {
			return findErr
		}
		if found == "" {
			return nil
		}
		srcFile = found
	}
	dstDir := claudeProjectDir(base, newCWD)
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return fmt.Errorf("create Claude project dir: %w", err)
	}
	if err := copyFileIfMissing(srcFile, filepath.Join(dstDir, sessionID+".jsonl")); err != nil {
		return err
	}

	srcSidecar := filepath.Join(filepath.Dir(srcFile), sessionID)
	if st, err := os.Stat(srcSidecar); err == nil && st.IsDir() {
		if err := copyDirIfMissing(srcSidecar, filepath.Join(dstDir, sessionID)); err != nil {
			return err
		}
	}
	return nil
}

func claudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func claudeProjectDir(base, cwd string) string { return filepath.Join(base, claudeProjectName(cwd)) }

func claudeProjectName(cwd string) string {
	var b strings.Builder
	for _, r := range filepath.Clean(cwd) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func findClaudeTranscript(base, sessionID string) (string, error) {
	target := sessionID + ".jsonl"
	var found string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) == target {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if os.IsNotExist(err) {
		return "", nil
	}
	return found, err
}

func copyFileIfMissing(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyDirIfMissing(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode())
		}
		return copyFileIfMissing(path, target)
	})
}
