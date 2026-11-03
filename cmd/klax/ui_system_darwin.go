package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// currentRSS returns the resident set size, or nil when unavailable. macOS has
// no /proc and no cgo-free syscall for it, so it asks ps.
func currentRSS() *uint64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return nil
	}
	kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return nil
	}
	bytes := kb * 1024
	return &bytes
}

// maxRSSBytes returns Rusage.Maxrss, which macOS reports in bytes (Linux: kilobytes).
func maxRSSBytes(u *syscall.Rusage) uint64 { return uint64(u.Maxrss) }
