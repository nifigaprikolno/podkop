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
	"io/fs"
	"log"
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

	// Router-facing API.
	mux.HandleFunc("/api/v1/profile", s.handleProfile)

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

// handleRobots keeps the operator area out of search results. The public site
// is only opened for indexing when the deployment asks for it — a devlog nobody
// links to is a weaker cover than one that is simply not crawled.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if s.cfg.SiteIndexing && s.cfg.Root == "site" {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: " + s.cfg.AdminPath + "\n"))
		return
	}
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

// handleRoot serves whatever the deployment put at the public root.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
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
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
