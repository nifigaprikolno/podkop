// Package hoststat reads a few host and process metrics for the panel's
// overview screen.
//
// Everything comes from /proc and the runtime, so the binary stays dependency
// free and cgo-less. On a host without /proc (or inside a sandbox that hides
// it) the affected fields are simply reported as unavailable — the panel must
// render either way.
package hoststat

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Stats is a snapshot of host and process state.
type Stats struct {
	Uptime     time.Duration
	StartedAt  time.Time
	Goroutines int
	// HeapMB is the Go heap in use; RSSMB is the resident set size of the
	// process (0 when /proc is unavailable).
	HeapMB float64
	RSSMB  float64

	// CPUPercent is the host CPU busy share between the two most recent
	// samples. Valid reports whether the sample could be taken at all.
	CPUPercent float64
	CPUValid   bool

	MemUsedMB  float64
	MemTotalMB float64
	MemValid   bool

	DiskUsedGB  float64
	DiskTotalGB float64
	DiskValid   bool
}

// Collector samples metrics. CPU usage needs two readings, so the collector
// keeps the previous one.
type Collector struct {
	procRoot  string
	diskPath  string
	startedAt time.Time

	mu       sync.Mutex
	prevIdle uint64
	prevAll  uint64
}

// New returns a collector reading the real /proc and measuring free space on
// the filesystem holding diskPath.
func New(diskPath string) *Collector {
	return &Collector{procRoot: "/proc", diskPath: diskPath, startedAt: time.Now()}
}

// WithProcRoot overrides the /proc location. Tests use it to simulate a host
// without /proc.
func (c *Collector) WithProcRoot(root string) *Collector {
	c.procRoot = root
	return c
}

// Collect takes a snapshot. It never fails: unavailable metrics are left
// zeroed with their Valid flag unset.
func (c *Collector) Collect() Stats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	s := Stats{
		Uptime:     time.Since(c.startedAt),
		StartedAt:  c.startedAt,
		Goroutines: runtime.NumGoroutine(),
		HeapMB:     float64(m.HeapAlloc) / (1 << 20),
	}

	s.RSSMB = c.readRSS()
	s.CPUPercent, s.CPUValid = c.readCPU()
	s.MemUsedMB, s.MemTotalMB, s.MemValid = c.readMem()
	s.DiskUsedGB, s.DiskTotalGB, s.DiskValid = c.readDisk()
	return s
}

// readCPU returns the busy share since the previous call. The first call has
// no baseline, so it reports invalid.
func (c *Collector) readCPU() (float64, bool) {
	b, err := os.ReadFile(filepath.Join(c.procRoot, "stat"))
	if err != nil {
		return 0, false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, false
	}

	var all, idle uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, false
		}
		all += v
		// Fields 4 and 5 (idle, iowait) count as not busy.
		if i == 3 || i == 4 {
			idle += v
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	prevAll, prevIdle := c.prevAll, c.prevIdle
	c.prevAll, c.prevIdle = all, idle
	if prevAll == 0 || all <= prevAll {
		return 0, false
	}
	deltaAll := float64(all - prevAll)
	deltaIdle := float64(idle - prevIdle)
	pct := (deltaAll - deltaIdle) / deltaAll * 100
	if pct < 0 {
		pct = 0
	}
	return pct, true
}

func (c *Collector) readMem() (used, total float64, ok bool) {
	b, err := os.ReadFile(filepath.Join(c.procRoot, "meminfo"))
	if err != nil {
		return 0, 0, false
	}
	var totalKB, availKB float64
	for _, line := range strings.Split(string(b), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			totalKB = v
		case "MemAvailable":
			availKB = v
		}
	}
	if totalKB == 0 {
		return 0, 0, false
	}
	return (totalKB - availKB) / 1024, totalKB / 1024, true
}

func (c *Collector) readRSS() float64 {
	b, err := os.ReadFile(filepath.Join(c.procRoot, "self", "statm"))
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0
	}
	return pages * float64(os.Getpagesize()) / (1 << 20)
}

func (c *Collector) readDisk() (used, total float64, ok bool) {
	path := c.diskPath
	if path == "" {
		return 0, 0, false
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		path = filepath.Dir(path)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, 0, false
	}
	const gb = 1 << 30
	totalB := float64(fs.Blocks) * float64(fs.Bsize)
	freeB := float64(fs.Bavail) * float64(fs.Bsize)
	if totalB == 0 {
		return 0, 0, false
	}
	return (totalB - freeB) / gb, totalB / gb, true
}
