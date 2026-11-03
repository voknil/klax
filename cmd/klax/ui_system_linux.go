package main

import (
	"fmt"
	"os"
	"syscall"
)

// currentRSS returns the resident set size from /proc, or nil when unavailable.
func currentRSS() *uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return nil
	}
	var virtual, resident uint64
	if _, err := fmt.Sscan(string(data), &virtual, &resident); err != nil {
		return nil
	}
	resident *= uint64(os.Getpagesize())
	return &resident
}

// maxRSSBytes converts Rusage.Maxrss, which Linux reports in kilobytes.
func maxRSSBytes(u *syscall.Rusage) uint64 { return uint64(u.Maxrss) * 1024 }
