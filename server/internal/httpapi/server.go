// Package httpapi wires podkop-server's HTTP surface:
//   - the public site on "/" — the Halogen devlog, which is also the panel's
//     cover story (or the legacy 4PDA decoy / a bare login form, see
//     config.Root),
//   - the operator area on a secret path: the devlog CMS plus the podkop panel
//     itself under its Extras tab,
//   - the /api/v1/profile endpoint that routers poll.
package httpapi

import (
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nifigaprikolno/podkop/server/internal/config"
	"github.com/nifigaprikolno/podkop/server/internal/eventlog"
	"github.com/nifigaprikolno/podkop/server/internal/hoststat"
	"github.com/nifigaprikolno/podkop/server/internal/store"
	"github.com/nifigaprikolno/podkop/server/internal/xui"
	"github.com/nifigaprikolno/podkop/server/web"
)

// BuildInfo describes the running binary; filled from ldflags in main.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// session is an authenticated operator session. Sessions live in memory only:
// a restart logs everyone out, which is acceptable for a single-operator panel
// and avoids persisting anything that grants access.
type session struct {
	csrf      string
	expiresAt time.Time
	ip        string
}

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg   *config.Config
	store *store.Store
	xui   *xui.Client // may be nil when 3x-UI integration is disabled

	events    *eventlog.Ring
	host      *hoststat.Collector
	build     BuildInfo
	startedAt time.Time

	decoyTmpl *template.Template
	decoyFS   fs.FS

	adminTmpl  *template.Template
	adminCSS   template.CSS
	adminFS    fs.FS
	assetFS    fs.FS
	siteTmpl   *template.Template
	siteCSS    template.CSS
	siteMedia  fs.FS
	loginGuard *loginGuard

	mu       sync.Mutex
	sessions map[string]*session
}

// NewServer constructs the HTTP server.
func NewServer(cfg *config.Config, st *store.Store, xc *xui.Client) (*Server, error) {
	s := &Server{
		cfg:        cfg,
		store:      st,
		xui:        xc,
		events:     eventlog.New(500),
		host:       hoststat.New(st.Path()),
		startedAt:  time.Now(),
		sessions:   map[string]*session{},
		loginGuard: newLoginGuard(cfg.LoginMaxFails, cfg.LoginLockout),
	}

	// Decoy assets: custom dir override or the embedded default.
	if cfg.DecoyDir != "" {
		s.decoyFS = os.DirFS(cfg.DecoyDir)
	} else {
		sub, err := fs.Sub(web.DecoyFS, "decoy")
		if err != nil {
			return nil, err
		}
		s.decoyFS = sub
	}
	indexHTML, err := fs.ReadFile(s.decoyFS, "index.html")
	if err != nil {
		return nil, err
	}
	s.decoyTmpl, err = template.New("decoy").Parse(string(indexHTML))
	if err != nil {
		return nil, err
	}

	if s.adminFS, err = fs.Sub(web.AdminFS, "admin"); err != nil {
		return nil, err
	}
	if s.adminTmpl, err = template.New("admin").Funcs(templateFuncs()).
		ParseFS(web.AdminFS, "admin/*.html"); err != nil {
		return nil, err
	}
	if s.assetFS, err = fs.Sub(s.adminFS, "assets"); err != nil {
		return nil, err
	}
	css, err := fs.ReadFile(s.adminFS, "panel.css")
	if err != nil {
		return nil, err
	}
	s.adminCSS = template.CSS(css)

	if s.siteTmpl, err = template.New("site").Funcs(templateFuncs()).
		ParseFS(web.SiteFS, "site/*.html"); err != nil {
		return nil, err
	}
	siteSub, err := fs.Sub(web.SiteFS, "site")
	if err != nil {
		return nil, err
	}
	siteCSS, err := fs.ReadFile(siteSub, "site.css")
	if err != nil {
		return nil, err
	}
	s.siteCSS = template.CSS(siteCSS)
	if s.siteMedia, err = fs.Sub(siteSub, "media"); err != nil {
		return nil, err
	}

	return s, nil
}

// Events exposes the panel's in-memory log so main can tee the standard logger
// into it.
func (s *Server) Events() *eventlog.Ring { return s.events }

// SetBuildInfo records version metadata for the config screen.
func (s *Server) SetBuildInfo(b BuildInfo) { s.build = b }

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Router-facing API, plus the same key as a subscription for phone clients.
	mux.HandleFunc("/api/v1/profile", s.handleProfile)
	mux.HandleFunc("/api/v1/sub", s.handleSubscription)

	// Operator area (CMS + panel) on the secret path.
	mux.HandleFunc(s.cfg.AdminPath, s.handleAdmin)

	// Public site.
	mux.HandleFunc("/robots.txt", s.handleRobots)
	mux.Handle("/media/", http.StripPrefix("/media/", s.mediaHandler()))
	mux.HandleFunc("/", s.handleRoot)

	return logRequests(mux)
}

// mediaHandler serves post images embedded in the binary. There is no upload
// path: media ships with the build, so a compromised panel cannot plant files.
func (s *Server) mediaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.siteMedia))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}

// contentSignalsPolicy is the Content Signals Policy preamble (contentsignals.org),
// the same text Cloudflare's managed robots.txt carries. It is served from the
// origin so the two files do not have to be stitched together: Cloudflare
// prepends its managed block before ours when the setting is on, and its
// "Allow: /" then contradicts our "Disallow: /" inside one and the same
// user-agent group. Carrying the policy here means the managed setting can be
// switched off without losing anything.
const contentSignalsPolicy = `# As a condition of accessing this website, you agree to abide by the
# following content signals:

# (a)  If a content-signal = yes, you may collect content for the
#      corresponding use.
# (b)  If a content-signal = no, you may not collect content for the
#      corresponding use.
# (c)  If the website operator does not include a content signal for a
#      corresponding use, the website operator neither grants nor restricts
#      permission via content signal with respect to the corresponding use.

# The content signals and their meanings are:

# search: building a search index and providing search results (e.g., returning
#         hyperlinks and short excerpts from your website's contents). Search
#         does not include providing AI-generated search summaries.
# ai-input: inputting content into one or more AI models (e.g., retrieval
#           augmented generation, grounding, or other real-time taking of
#           content for generative AI search answers).
# ai-train: training or fine-tuning AI models.

# ANY RESTRICTIONS EXPRESSED VIA CONTENT SIGNALS ARE EXPRESS RESERVATIONS OF
# RIGHTS UNDER ARTICLE 4 OF THE EUROPEAN UNION DIRECTIVE 2019/790 ON COPYRIGHT
# AND RELATED RIGHTS IN THE DIGITAL SINGLE MARKET.
`

// aiCrawlers are the model-training and answer-engine bots that get a plain
// refusal even when the site itself is open to search engines — the same intent
// as Cloudflare's managed rules, spelled out at the origin. A wildcard
// "Disallow: /" already covers them when indexing is off, so the list is only
// emitted in the open case.
var aiCrawlers = []string{
	"AI2Bot", "Amazonbot", "Applebot-Extended", "Bytespider", "CCBot",
	"ChatGPT-User", "Claude-SearchBot", "Claude-User", "ClaudeBot",
	"Diffbot", "FacebookBot", "GPTBot", "Google-Extended", "ImagesiftBot",
	"Meta-ExternalAgent", "OAI-SearchBot", "PerplexityBot", "Perplexity-User",
	"Timpibot", "Webzio-Extended", "anthropic-ai", "cohere-ai",
}

// handleRobots states the crawling preference for the public site. The operator
// area is never named here: robots.txt is world-readable, so a Disallow line
// would publish the secret admin path to everyone who asks for the file — and
// to every scraper that harvests robots.txt looking for exactly that. The area
// is kept out of search results by the X-Robots-Tag header on its own
// responses instead, which discloses nothing.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	var b strings.Builder
	b.WriteString(contentSignalsPolicy)
	b.WriteString("\nUser-agent: *\n")

	if s.cfg.SiteIndexing && s.cfg.Root == "site" {
		b.WriteString("Content-Signal: search=yes, ai-input=no, ai-train=no, use=reference\n")
		b.WriteString("Allow: /\n\n")
		for _, ua := range aiCrawlers {
			b.WriteString("User-agent: " + ua + "\n")
		}
		b.WriteString("Disallow: /\n")
	} else {
		b.WriteString("Content-Signal: search=no, ai-input=no, ai-train=no, use=immediate\n")
		b.WriteString("Disallow: /\n")
	}

	_, _ = io.WriteString(w, b.String())
}

// handleRoot serves whatever the deployment put at the public root — unless the
// request arrived on the dedicated operator hostname, which goes to the panel.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.isAdminHost(r) {
		s.noIndex(w)
		http.Redirect(w, r, s.cfg.AdminPath, http.StatusSeeOther)
		return
	}

	switch s.cfg.Root {
	case "decoy":
		s.handleDecoy(w, r)
	case "login":
		s.noIndex(w)
		s.adminLogin(w, r)
	default:
		s.handleSite(w, r)
	}
}

// isAdminHost reports whether the request came in on the operator hostname.
// The forwarded header is honoured only behind a trusted proxy: otherwise a
// spoofed header would hand the secret admin path to anyone who asks.
func (s *Server) isAdminHost(r *http.Request) bool {
	if s.cfg.AdminHost == "" {
		return false
	}
	host := r.Host
	if s.cfg.TrustedProxy {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = fh
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, s.cfg.AdminHost)
}

// adminReachable reports whether the operator area answers on this request's
// hostname. With AdminLocalOnly off it answers everywhere, which is the old
// behaviour; with it on, only a loopback Host — an SSH tunnel to the VPS — and
// AdminHost get through.
//
// The decision is on the Host rather than the peer address on purpose: the
// server usually runs in a container, so a request forwarded from a loopback
// port on the host arrives from the bridge gateway and never looks local.
// A forged Host cannot help an attacker here, because a request that reaches
// this process through Cloudflare carries the hostname the tunnel routed it to,
// and that hostname is what the check reads.
func (s *Server) adminReachable(r *http.Request) bool {
	if !s.cfg.AdminLocalOnly {
		return true
	}
	return s.isAdminHost(r) || s.requestIsLocal(r)
}

// extrasReachable reports whether the podkop panel — as opposed to the devlog
// CMS next to it — answers on this request. AdminHost does not count here: the
// point of the option is that clients and keys are reached through a tunnel,
// and a hostname on the internet is not one however well it is guarded.
func (s *Server) extrasReachable(r *http.Request) bool {
	return !s.cfg.ExtrasLocalOnly || s.requestIsLocal(r)
}

// requestIsLocal reports whether the request arrived on a loopback Host — which
// in practice means through an SSH tunnel to the VPS.
func (s *Server) requestIsLocal(r *http.Request) bool {
	// A request that came through Cloudflare is not local whatever it claims.
	if r.Header.Get("CF-Ray") != "" || r.Header.Get("CF-Connecting-IP") != "" {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}

// noIndex marks a response as off-limits for crawlers.
func (s *Server) noIndex(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
}

// handleDecoy serves the camouflage site. GET renders the login page; POST to the
// login form is a dead end that always returns a plausible "wrong credentials"
// message — it never validates or stores anything. The real operator login lives
// on the secret admin path.
func (s *Server) handleDecoy(w http.ResponseWriter, r *http.Request) {
	// Serve static assets (css, images) directly if present.
	if r.Method == http.MethodGet && r.URL.Path != "/" {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "index.html" {
			if f, err := s.decoyFS.Open(clean); err == nil {
				f.Close()
				http.FileServer(http.FS(s.decoyFS)).ServeHTTP(w, r)
				return
			}
		}
	}

	data := struct{ Error string }{}
	if r.Method == http.MethodPost {
		// Dead form: never authenticate, just look like a failed login.
		data.Error = "Неверный логин или пароль"
		w.WriteHeader(http.StatusOK)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.decoyTmpl.Execute(w, data); err != nil {
		log.Printf("decoy render: %v", err)
	}
}

// logRequests logs method + path but never query strings (they carry tokens).
// The duration is what the server itself spent: when the panel feels slow over
// a tunnel, this is the number that says whether the time went here or on the
// way here.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
	})
}
