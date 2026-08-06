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
	"strings"
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
	// UpdatedAt is stamped on every save. Compared against Client.LastSeen it
	// tells whether a router is still running an older revision of the profile.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
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
	// Pull telemetry, updated whenever the router fetches its profile. This is
	// the only "liveness" signal the panel has — there is no persistent
	// connection between a router and the server.
	LastSeen  time.Time `json:"last_seen,omitempty"`
	LastIP    string    `json:"last_ip,omitempty"`
	PullCount int       `json:"pull_count,omitempty"`
}

// Post is a devlog entry of the public site. The site is the panel's cover: a
// real project log with real content, edited from the same admin area.
type Post struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Summary is the teaser shown in the feed; Body is Markdown.
	Summary string `json:"summary"`
	Body    string `json:"body"`
	// Cover is a path under /media/ or an absolute URL.
	Cover     string    `json:"cover,omitempty"`
	Tag       string    `json:"tag,omitempty"`
	Published bool      `json:"published"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type data struct {
	Profiles map[string]*Profile `json:"profiles"`
	Clients  map[string]*Client  `json:"clients"`
	Posts    map[string]*Post    `json:"posts"`
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
			Posts:    map[string]*Post{},
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
	if s.d.Posts == nil {
		s.d.Posts = map[string]*Post{}
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

// Reload re-reads the store file from disk, discarding the in-memory copy. Used
// by the panel when the file was edited out of band.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var d data
	if len(b) > 0 {
		if err := json.Unmarshal(b, &d); err != nil {
			return fmt.Errorf("parse store %s: %w", s.path, err)
		}
	}
	if d.Profiles == nil {
		d.Profiles = map[string]*Profile{}
	}
	if d.Clients == nil {
		d.Clients = map[string]*Client{}
	}
	if d.Posts == nil {
		d.Posts = map[string]*Post{}
	}
	s.d = d
	return nil
}

// Path returns the store file location (shown on the panel's config screen).
func (s *Store) Path() string { return s.path }

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
	p.UpdatedAt = time.Now().UTC()
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

// TouchClient records a successful profile pull: last seen time, source IP and
// the pull counter. Unknown tokens are ignored.
func (s *Store) TouchClient(token, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.d.Clients[token]
	if !ok {
		return ErrNotFound
	}
	c.LastSeen = time.Now().UTC()
	c.LastIP = ip
	c.PullCount++
	return s.save()
}

// DeleteClient removes a client.
func (s *Store) DeleteClient(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.d.Clients, token)
	return s.save()
}

// ---- Posts ----

// PutPost creates or replaces a devlog post. A missing id is generated, and a
// missing or colliding slug is derived from the title.
func (s *Store) PutPost(p *Post) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if p.ID == "" {
		id, err := NewID(8)
		if err != nil {
			return err
		}
		p.ID = id
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	p.Slug = Slugify(p.Slug)
	if p.Slug == "" {
		p.Slug = Slugify(p.Title)
	}
	if p.Slug == "" {
		p.Slug = "post-" + p.ID
	}
	// Slugs address posts in URLs, so they must stay unique.
	base := p.Slug
	for i := 2; ; i++ {
		clash := false
		for _, other := range s.d.Posts {
			if other.ID != p.ID && other.Slug == p.Slug {
				clash = true
				break
			}
		}
		if !clash {
			break
		}
		p.Slug = fmt.Sprintf("%s-%d", base, i)
	}

	s.d.Posts[p.ID] = p
	return s.save()
}

// Post returns a copy of the post with the given id.
func (s *Store) Post(id string) (*Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.d.Posts[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

// PostBySlug returns a copy of the post addressed by its slug.
func (s *Store) PostBySlug(slug string) (*Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.d.Posts {
		if p.Slug == slug {
			cp := *p
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// Posts returns posts newest first. When publishedOnly is set, drafts are
// skipped — that is what the public site asks for.
func (s *Store) Posts(publishedOnly bool) []*Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Post, 0, len(s.d.Posts))
	for _, p := range s.d.Posts {
		if publishedOnly && !p.Published {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// DeletePost removes a post.
func (s *Store) DeletePost(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.d.Posts, id)
	return s.save()
}

// Slugify turns a title into a URL-safe slug. Cyrillic is transliterated so
// Russian titles produce readable URLs instead of empty strings.
func Slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case translit[r] != "":
			b.WriteString(translit[r])
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "",
	'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// EnsureSeedPosts writes two starter devlog entries when the store has none.
// The public site is the panel's cover story, and an empty log is a poor one —
// the operator is expected to replace these with real entries.
func (s *Store) EnsureSeedPosts() error {
	s.mu.RLock()
	empty := len(s.d.Posts) == 0
	s.mu.RUnlock()
	if !empty {
		return nil
	}

	seeds := []*Post{
		{
			Title:   "The garage scene, and why it came first",
			Slug:    "garage-scene",
			Tag:     "tooling",
			Summary: "Before any city, there is a lift, a camera rig and a car that can be taken apart.",
			Body: "Every racing game I have started before died in the same place: a city got blocked out, " +
				"nothing drove well, and the whole thing lost its point. So this one starts inside a garage.\n\n" +
				"The scene is a single room with a lift, a fixed camera rig and one hatchback. That is enough to " +
				"work on the parts that actually decide whether the game is fun:\n\n" +
				"- the gearbox and the torque curve\n" +
				"- how the car settles when it lands\n" +
				"- what the player is told about it, and how fast\n\n" +
				"In the editor it is all collision volumes and a camera track. Ugly, and exactly as " +
				"complicated as it needs to be.",
			Published: true,
		},
		{
			Title:   "Tuning UI: numbers you can feel",
			Slug:    "tuning-ui",
			Tag:     "ui",
			Cover:   "/media/tuning-reference.jpg",
			Summary: "A torque graph is only useful if the next pull tells you the same story.",
			Body: "The tuning screen is a reference more than a design: pick a part, see the curve move, " +
				"drive and feel whether the change was worth it.\n\n" +
				"Two rules came out of the first pass:\n\n" +
				"1. Every number on screen has to correspond to something the car does.\n" +
				"2. Anything live — speed, gear, RPM — renders on a seven-segment face, so it reads as an " +
				"instrument rather than as text.\n\n" +
				"The graph is still a placeholder pulled from an old game as a layout reference. The values " +
				"behind it are real.",
			Published: true,
		},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i, p := range seeds {
		id, err := NewID(8)
		if err != nil {
			return err
		}
		p.ID = id
		// Keep the newest first when the feed sorts by creation time.
		p.CreatedAt = now.Add(-time.Duration(len(seeds)-i) * 24 * time.Hour)
		p.UpdatedAt = now
		s.d.Posts[p.ID] = p
	}
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
		UpdatedAt:      time.Now().UTC(),
	}
	s.d.Profiles[p.ID] = p
	if err := s.save(); err != nil {
		return nil, err
	}
	cp := *p
	return &cp, nil
}
