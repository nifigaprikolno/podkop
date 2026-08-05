// Package httpapi wires podkop-server's HTTP surface:
//   - a decoy site on "/" (camouflage for active probing),
//   - the operator admin panel on a secret path,
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

	"github.com/nifigaprikolno/podkop/server/internal/config"
	"github.com/nifigaprikolno/podkop/server/internal/store"
	"github.com/nifigaprikolno/podkop/server/internal/xui"
	"github.com/nifigaprikolno/podkop/server/web"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg   *config.Config
	store *store.Store
	xui   *xui.Client // may be nil when 3x-UI integration is disabled

	decoyTmpl *template.Template
	decoyFS   fs.FS

	adminTmpl *template.Template

	mu       sync.Mutex
	sessions map[string]bool // active admin session ids
}

// NewServer constructs the HTTP server.
func NewServer(cfg *config.Config, st *store.Store, xc *xui.Client) (*Server, error) {
	s := &Server{
		cfg:      cfg,
		store:    st,
		xui:      xc,
		sessions: map[string]bool{},
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

	s.adminTmpl, err = template.New("admin").Funcs(template.FuncMap{
		"join": strings.Join,
	}).Parse(adminTemplates)
	if err != nil {
		return nil, err
	}

	return s, nil
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Router-facing API.
	mux.HandleFunc("/api/v1/profile", s.handleProfile)

	// Admin panel on the secret path.
	mux.HandleFunc(s.cfg.AdminPath, s.handleAdmin)

	// Everything else is the decoy.
	mux.HandleFunc("/", s.handleDecoy)

	return logRequests(mux)
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
