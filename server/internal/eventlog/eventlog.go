// Package eventlog is a small in-memory ring buffer of panel events.
//
// podkop-server has no log storage: it writes to stderr and the container
// swallows the history. The panel's LOGS and RECENT EVENTS screens need
// something to show, so the same lines are teed into a bounded ring here.
// Nothing is persisted — a restart starts with an empty buffer, which is the
// honest behaviour for a process-local log.
package eventlog

import (
	"bytes"
	"strings"
	"sync"
	"time"
)

// Level of an entry. Kept as plain strings because they are rendered directly
// and filtered by the UI.
const (
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// Entry is a single recorded event.
type Entry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Tag   string    `json:"tag"`
	Text  string    `json:"text"`
}

// Ring is a fixed-capacity, thread-safe event buffer. It also implements
// io.Writer so it can be attached to the standard logger.
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	filled  bool
	now     func() time.Time
}

// New returns a ring holding at most capacity entries.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{entries: make([]Entry, capacity), now: time.Now}
}

// Add records an entry.
func (r *Ring) Add(level, tag, text string) {
	if text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = Entry{Time: r.now().UTC(), Level: level, Tag: tag, Text: text}
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.filled = true
	}
}

// Info, Warn and Error are shorthands for Add.
func (r *Ring) Info(tag, text string)  { r.Add(LevelInfo, tag, text) }
func (r *Ring) Warn(tag, text string)  { r.Add(LevelWarn, tag, text) }
func (r *Ring) Error(tag, text string) { r.Add(LevelError, tag, text) }

// Write implements io.Writer so the ring can sit in the standard logger's
// output next to stderr. The level is guessed from the message text, since the
// standard logger carries no level of its own.
func (r *Ring) Write(p []byte) (int, error) {
	n := len(p)
	for _, line := range bytes.Split(bytes.TrimRight(p, "\n"), []byte{'\n'}) {
		text := strings.TrimSpace(string(line))
		if text == "" {
			continue
		}
		r.Add(guessLevel(text), "LOG", text)
	}
	return n, nil
}

func guessLevel(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "error"), strings.Contains(lower, "failed"),
		strings.Contains(lower, "fail:"), strings.Contains(lower, "panic"):
		return LevelError
	case strings.Contains(lower, "warn"), strings.Contains(lower, "denied"),
		strings.Contains(lower, "invalid"), strings.Contains(lower, "locked"):
		return LevelWarn
	default:
		return LevelInfo
	}
}

// Entries returns up to limit entries, newest first. An empty level (or "ALL")
// returns every level.
func (r *Ring) Entries(level string, limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	size := len(r.entries)
	count := r.next
	if r.filled {
		count = size
	}
	if limit <= 0 || limit > count {
		limit = count
	}

	out := make([]Entry, 0, limit)
	for i := 0; i < count && len(out) < limit; i++ {
		// Walk backwards from the most recently written slot.
		idx := (r.next - 1 - i + size*2) % size
		e := r.entries[idx]
		if e.Time.IsZero() {
			continue
		}
		if level != "" && level != "ALL" && e.Level != level {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Len reports how many entries are currently buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.filled {
		return len(r.entries)
	}
	return r.next
}
