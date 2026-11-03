package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/PiDmitrius/klax/internal/pathutil"
)

// installBinary copies the running executable to ~/.local/bin/klax and returns
// the destination path. Shared by the Linux (systemd) and macOS (launchd)
// install flows.
func installBinary() string {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find executable: %v\n", err)
		os.Exit(1)
	}

	home, _ := os.UserHomeDir()
	binDir := filepath.Join(home, ".local", "bin")
	os.MkdirAll(binDir, 0755)
	dst := filepath.Join(binDir, "klax")
	if err := copyFile(exe, dst, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot install: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("installed: %s\n", pathutil.TildePathsInText(dst))
	return dst
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".klax-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := os.Rename(out.Name(), dst); err != nil {
		return err
	}
	return dir.Sync()
}

// unitDrifted reports whether the file at path differs from the expected
// content. A missing file is not drift. Used for both the systemd unit and the
// launchd plist.
func unitDrifted(path, expected string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !bytes.Equal(data, []byte(expected)), nil
}
