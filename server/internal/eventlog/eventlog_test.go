package eventlog

import (
	"log"
	"strings"
	"testing"
)

func TestRingKeepsNewestWithinCapacity(t *testing.T) {
	r := New(3)
	for _, text := range []string{"one", "two", "three", "four"} {
		r.Info("TEST", text)
	}

	if got := r.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	entries := r.Entries("", 0)
	want := []string{"four", "three", "two"} // newest first, "one" evicted
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, w := range want {
		if entries[i].Text != w {
			t.Errorf("entry %d = %q, want %q", i, entries[i].Text, w)
		}
	}
}

func TestRingFiltersByLevelAndLimit(t *testing.T) {
	r := New(10)
	r.Info("A", "info line")
	r.Warn("B", "warn line")
	r.Error("C", "error line")

	if got := r.Entries(LevelWarn, 0); len(got) != 1 || got[0].Text != "warn line" {
		t.Errorf("level filter = %+v, want the single warn entry", got)
	}
	if got := r.Entries("ALL", 2); len(got) != 2 {
		t.Errorf("limit = %d entries, want 2", len(got))
	}
	if got := r.Entries("", 0); len(got) != 3 {
		t.Errorf("empty level should not filter, got %d entries", len(got))
	}
}

// The ring doubles as the standard logger's sink, so log output must show up as
// entries with a level guessed from the text.
func TestRingAsLogWriter(t *testing.T) {
	r := New(10)
	logger := log.New(r, "", 0)
	logger.Print("issue xui key: connection failed")
	logger.Print("client created")

	entries := r.Entries("", 0)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Level != LevelInfo || !strings.Contains(entries[0].Text, "client created") {
		t.Errorf("newest entry = %+v, want an INFO about the created client", entries[0])
	}
	if entries[1].Level != LevelError {
		t.Errorf("failure line level = %q, want %q", entries[1].Level, LevelError)
	}
}

func TestRingIgnoresBlankWrites(t *testing.T) {
	r := New(4)
	if _, err := r.Write([]byte("\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r.Info("TAG", "")
	if got := r.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
}
