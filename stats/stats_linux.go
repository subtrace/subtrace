package stats

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

var mu sync.RWMutex
var data = make(map[string]string)

func Loop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	default:
		tick()
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

func tick() {
	m := make(map[string]string)

	m["subtrace_linux_cpu_count"] = fmt.Sprintf("%d", runtime.NumCPU())

	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Split(string(b), " "); len(fields) >= 3 {
			m["subtrace_linux_cpu_load"] = strings.Join(fields[0:3], " ")
		}
	}

	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if fields := strings.Fields(line); len(fields) >= 2 {
				val := strings.Join(fields[1:], " ")
				switch strings.TrimSuffix(fields[0], ":") {
				case "MemTotal":
					m["subtrace_linux_mem_total"] = val
				case "MemAvailable":
					m["subtrace_linux_mem_available"] = val
				}
			}
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		data[k] = v
	}
}

func Load() map[string]string {
	mu.RLock()
	defer mu.RUnlock()

	m := make(map[string]string)
	for k, v := range data {
		m[k] = v
	}
	return m
}
