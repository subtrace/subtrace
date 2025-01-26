package stats

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

type Stats struct {
	cpuCount string

	loadAvg1Min  string
	loadAvg5Min  string
	loadAvg15Min string

	memTotal     string
	memFree      string
	memAvailable string

	mu sync.RWMutex
}

func New(ctx context.Context) *Stats {
	s := new(Stats)

	count, err := getCPUCount()
	if err != nil {
		// This isn't critical, so we just log.
		slog.Info("failed to get cpu count", "err", err)
		s.cpuCount = ""
	} else {
		s.cpuCount = fmt.Sprintf("%d", count)
	}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.tick()
			case <-ctx.Done():
				return
			}
		}
	}()

	return s

}

func (s *Stats) tick() {
	loadAvg1Min, loadAvg5Min, loadAvg15Min, err := getLoadAvgInfo()
	if err != nil {
		// This isn't critical, so we just log.
		slog.Info("failed to get loadavg", "err", err)
		loadAvg1Min = ""
		loadAvg5Min = ""
		loadAvg15Min = ""
	}

	memTotal, memFree, memAvailable, err := getMemInfo()
	if err != nil {
		// This isn't critical, so we just log.
		slog.Info("failed to get meminfo", "err", err)
		memTotal = ""
		memFree = ""
		memAvailable = ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadAvg1Min = loadAvg1Min
	s.loadAvg5Min = loadAvg5Min
	s.loadAvg15Min = loadAvg15Min
	s.memTotal = memTotal
	s.memFree = memFree
	s.memAvailable = memAvailable

}

func (s *Stats) GetStatsTags() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]string{
		"subtrace_linux_cpu_count":             s.cpuCount,
		"subtrace_linux_loadavg_1_min":         s.loadAvg1Min,
		"subtrace_linux_loadavg_5_min":         s.loadAvg5Min,
		"subtrace_linux_loadavg_15_min":        s.loadAvg15Min,
		"subtrace_linux_meminfo_mem_total":     s.memTotal,
		"subtrace_linux_meminfo_mem_free":      s.memFree,
		"subtrace_linux_meminfo_mem_available": s.memAvailable,
	}
}

func getCPUCount() (int, error) {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0, fmt.Errorf("read /proc/cpuinfo: %w", err)
	}

	blocks := strings.Split(string(b), "\n\n")
	return len(blocks) - 1, nil
}

// From [1]:
//
// /proc/loadavg
// The first three fields in this file are load average figures giving the number
// of jobs in the run queue (state R) or waiting for disk I/O (state D) averaged
// over 1, 5, and 15 minutes.
//
// [1] https://man7.org/linux/man-pages/man5/proc_loadavg.5.html
func getLoadAvgInfo() (string, string, string, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "", "", "", fmt.Errorf("read /proc/loadavg: %w", err)
	}

	values := strings.Split(string(b), " ")
	if len(values) < 3 {
		return "", "", "", fmt.Errorf("not enough info in /proc/loadavg, expected at least 3 values but got %d", len(values))
	}

	return values[0], values[1], values[2], nil

}

// See [1] for details about the /proc/meminfo file.
//
// [1] https://man7.org/linux/man-pages/man5/proc_meminfo.5.html
func getMemInfo() (string, string, string, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "", "", "", fmt.Errorf("read: %w", err)
	}

	var memTotal, memFree, memAvailable string
	lines := strings.Split(string(b), "\n")

	for _, l := range lines {
		words := strings.Fields(l)
		if len(words) < 2 {
			continue
		}

		key := words[0]
		value := strings.Join(words[1:], " ")
		switch key {
		case "MemTotal:":
			memTotal = value
		case "MemFree:":
			memFree = value
		case "MemAvailable:":
			memAvailable = value
		}
	}

	return memTotal, memFree, memAvailable, nil
}
