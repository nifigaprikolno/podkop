package hoststat

import (
	"testing"
	"time"
)

func TestCollectRealHost(t *testing.T) {
	c := New(t.TempDir())
	c.Collect() // first sample seeds the CPU baseline
	time.Sleep(50 * time.Millisecond)
	s := c.Collect()

	if s.Uptime <= 0 {
		t.Errorf("uptime = %v, want > 0", s.Uptime)
	}
	if s.Goroutines < 1 {
		t.Errorf("goroutines = %d, want >= 1", s.Goroutines)
	}
	if !s.DiskValid || s.DiskTotalGB <= 0 {
		t.Errorf("disk stats unavailable: %+v", s)
	}
	if s.CPUValid && (s.CPUPercent < 0 || s.CPUPercent > 100) {
		t.Errorf("cpu = %v, want 0..100", s.CPUPercent)
	}
}

// A host without /proc must degrade to "unavailable", not panic: the panel has
// to render on any platform the binary happens to run on.
func TestCollectWithoutProc(t *testing.T) {
	s := New(t.TempDir()).WithProcRoot(t.TempDir() + "/absent").Collect()

	if s.CPUValid || s.MemValid {
		t.Errorf("expected cpu/mem to be unavailable, got %+v", s)
	}
	if s.RSSMB != 0 {
		t.Errorf("RSSMB = %v, want 0 without /proc", s.RSSMB)
	}
	if s.Goroutines < 1 || s.Uptime <= 0 {
		t.Errorf("runtime-sourced fields should still be filled: %+v", s)
	}
}
