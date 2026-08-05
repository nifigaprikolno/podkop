// Package store is a small thread-safe, JSON-file-backed persistence layer for
// podkop-server. It intentionally avoids an external database driver so the
// binary stays dependency-free and trivially cross-compiles (e.g. to aarch64)
// without cgo. For larger deployments this can be swapped for SQLite behind the
// same interface.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Profile is a named set of route settings distributed to routers.
type Profile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	CommunityLists []string `json:"community_lists"`
	P2PDirect      bool     `json:"p2p_direct"`
	DirectRUZones  bool     `json:"direct_ru_zones"`
	DNSType        string   `json:"dns_type"`
	DNSServer      string   `json:"dns_server"`
	UpdateInterval string   `json:"update_interval"`
}

// Client is a router that authenticates with a token and receives a proxy key
// plus an assigned route profile.
type Client struct {
	Token       string    `json:"token"`
	Name        string    `json:"name"`
	ProxyString string    `json:"proxy_string"`
	ProfileID   string    `json:"profile_id"`
	XUIEmail    string    `json:"xui_email,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

type data struct {
	Profiles map[string]*Profile `json:"profiles"`
	Clients  map[string]*Client  `json:"clients"`
}

// Store persists profiles and clients to a JSON file.
type Store struct {
	mu   sync.RWMutex
	path string
	d    data
}

// ErrNotFound is returned when a lookup fails.
var ErrNotFound = errors.New("not found")

// Open loads (or initializes) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		d: data{
			Profiles: map[string]*Profile{},
			Clients:  map[string]*Client{},
		},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.save()
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("parse store %s: %w", path, err)
		}
	}
	if s.d.Profiles == nil {
		s.d.Profiles = map[string]*Profile{}
	}
	if s.d.Clients == nil {
		s.d.Clients = map[string]*Client{}
	}
	return s, nil
}

// save writes the store atomically. Callers must hold the write lock.
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// NewID returns a random hex identifier/token.
func NewID(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---- Profiles ----

// PutProfile creates or replaces a profile.
func (s *Store) PutProfile(p *Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		id, err := NewID(8)
		if err != nil {
			return err
		}
		p.ID = id
	}
	s.d.Profiles[p.ID] = p
	return s.save()
}

// Profile returns a copy of the profile with the given id.
func (s *Store) Profile(id string) (*Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.d.Profiles[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

// Profiles returns all profiles sorted by name.
func (s *Store) Profiles() []*Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Profile, 0, len(s.d.Profiles))
	for _, p := range s.d.Profiles {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeleteProfile removes a profile.
func (s *Store) DeleteProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.d.Profiles, id)
	return s.save()
}

// ---- Clients ----

// PutClient creates or replaces a client. A missing token is generated.
func (s *Store) PutClient(c *Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Token == "" {
		tok, err := NewID(16)
		if err != nil {
			return err
		}
		c.Token = tok
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	s.d.Clients[c.Token] = c
	return s.save()
}

// Client returns a copy of the client with the given token.
func (s *Store) Client(token string) (*Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.d.Clients[token]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

// Clients returns all clients sorted by creation time (newest first).
func (s *Store) Clients() []*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Client, 0, len(s.d.Clients))
	for _, c := range s.d.Clients {
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// DeleteClient removes a client.
func (s *Store) DeleteClient(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.d.Clients, token)
	return s.save()
}

// EnsureDefaultProfile seeds the curated default profile if no profiles exist.
// It mirrors the client's out-of-the-box configuration.
func (s *Store) EnsureDefaultProfile() (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.d.Profiles) > 0 {
		// Return any existing profile deterministically.
		ids := make([]string, 0, len(s.d.Profiles))
		for id := range s.d.Profiles {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		cp := *s.d.Profiles[ids[0]]
		return &cp, nil
	}
	p := &Profile{
		ID:             "default",
		Name:           "Default (Russia)",
		CommunityLists: []string{"russia_inside"},
		P2PDirect:      true,
		DirectRUZones:  true,
		DNSType:        "doh",
		DNSServer:      "https://dns.google/dns-query",
		UpdateInterval: "1d",
	}
	s.d.Profiles[p.ID] = p
	if err := s.save(); err != nil {
		return nil, err
	}
	cp := *p
	return &cp, nil
}
