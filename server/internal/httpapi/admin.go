package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/nifigaprikolno/podkop/server/internal/store"
	"github.com/nifigaprikolno/podkop/server/internal/xui"
)

const sessionCookie = "pk_admin"

// handleAdmin dispatches all requests under the secret admin path.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, s.cfg.AdminPath)
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case "", "login":
		s.adminLogin(w, r)
		return
	case "logout":
		s.adminLogout(w, r)
		return
	}

	// Everything below requires an authenticated session.
	if !s.authed(r) {
		http.Redirect(w, r, s.cfg.AdminPath, http.StatusSeeOther)
		return
	}

	switch rel {
	case "dashboard":
		s.adminDashboard(w, r, "")
	case "clients/create":
		s.adminCreateClient(w, r)
	case "clients/delete":
		s.adminDeleteClient(w, r)
	case "clients/toggle":
		s.adminToggleClient(w, r)
	case "profiles/save":
		s.adminSaveProfile(w, r)
	case "profiles/delete":
		s.adminDeleteProfile(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[c.Value]
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if s.authed(r) {
		http.Redirect(w, r, s.cfg.AdminPath+"dashboard", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		user := r.FormValue("username")
		pass := r.FormValue("password")
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) == 1
		if userOK && passOK {
			sid := mustToken(24)
			s.mu.Lock()
			s.sessions[sid] = true
			s.mu.Unlock()
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookie,
				Value:    sid,
				Path:     s.cfg.AdminPath,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   r.TLS != nil,
			})
			http.Redirect(w, r, s.cfg.AdminPath+"dashboard", http.StatusSeeOther)
			return
		}
		s.render(w, "login", map[string]any{"AdminPath": s.cfg.AdminPath, "Error": "Неверные учётные данные"})
		return
	}
	s.render(w, "login", map[string]any{"AdminPath": s.cfg.AdminPath})
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: s.cfg.AdminPath, MaxAge: -1})
	http.Redirect(w, r, s.cfg.AdminPath, http.StatusSeeOther)
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request, notice string) {
	profiles := s.store.Profiles()
	clients := s.store.Clients()
	// Attach a subscription URL hint per client for convenience.
	base := publicBaseURL(r)
	type clientView struct {
		*store.Client
		ProfileName string
		SubURL      string
	}
	profByID := map[string]string{}
	for _, p := range profiles {
		profByID[p.ID] = p.Name
	}
	cviews := make([]clientView, 0, len(clients))
	for _, c := range clients {
		cviews = append(cviews, clientView{
			Client:      c,
			ProfileName: profByID[c.ProfileID],
			SubURL:      fmt.Sprintf("%s/api/v1/profile?token=%s", base, c.Token),
		})
	}
	s.render(w, "dashboard", map[string]any{
		"AdminPath":  s.cfg.AdminPath,
		"Profiles":   profiles,
		"Clients":    cviews,
		"XUIEnabled": s.xui != nil,
		"Notice":     notice,
	})
}

func (s *Server) adminCreateClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, s.cfg.AdminPath+"dashboard", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	profileID := r.FormValue("profile_id")
	mode := r.FormValue("mode")
	proxy := strings.TrimSpace(r.FormValue("proxy_string"))

	if name == "" {
		s.adminDashboard(w, r, "Client name is required")
		return
	}

	var xuiEmail string
	if mode == "xui" {
		if s.xui == nil {
			s.adminDashboard(w, r, "3x-UI integration is not configured")
			return
		}
		link, email, err := s.issueXUIKey(r.Context(), name)
		if err != nil {
			log.Printf("issue xui key: %v", err)
			s.adminDashboard(w, r, "Failed to issue key via 3x-UI: "+err.Error())
			return
		}
		proxy = link
		xuiEmail = email
	}

	if proxy == "" {
		s.adminDashboard(w, r, "Provide a proxy link or use 3x-UI")
		return
	}

	c := &store.Client{
		Name:        name,
		ProxyString: proxy,
		ProfileID:   profileID,
		XUIEmail:    xuiEmail,
		Enabled:     true,
	}
	if err := s.store.PutClient(c); err != nil {
		s.adminDashboard(w, r, "Failed to save client: "+err.Error())
		return
	}
	s.adminDashboard(w, r, "Client created. Key/token: "+c.Token)
}

// issueXUIKey logs into 3x-UI, creates a client on the configured inbound and
// returns the generated vless link and the client's email.
func (s *Server) issueXUIKey(ctx context.Context, name string) (link, email string, err error) {
	if s.cfg.XUIInbound == 0 {
		return "", "", fmt.Errorf("XUI_INBOUND_ID is not set")
	}
	if err := s.xui.Login(ctx); err != nil {
		return "", "", err
	}
	uuid, err := newUUID()
	if err != nil {
		return "", "", err
	}
	email = fmt.Sprintf("%s-%s", sanitizeEmail(name), mustToken(3))
	cl := xui.NewClientSettings(uuid, email)
	if err := s.xui.AddClient(ctx, s.cfg.XUIInbound, cl); err != nil {
		return "", "", err
	}
	in, err := s.xui.GetInbound(ctx, s.cfg.XUIInbound)
	if err != nil {
		return "", "", err
	}
	link, err = s.xui.BuildVlessLink(in, cl)
	if err != nil {
		return "", "", err
	}
	return link, email, nil
}

func (s *Server) adminDeleteClient(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		_ = s.store.DeleteClient(r.FormValue("token"))
	}
	s.adminDashboard(w, r, "Client deleted")
}

func (s *Server) adminToggleClient(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if c, err := s.store.Client(r.FormValue("token")); err == nil {
			c.Enabled = !c.Enabled
			_ = s.store.PutClient(c)
		}
	}
	s.adminDashboard(w, r, "Client updated")
}

func (s *Server) adminSaveProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, s.cfg.AdminPath+"dashboard", http.StatusSeeOther)
		return
	}
	p := &store.Profile{
		ID:             r.FormValue("id"),
		Name:           strings.TrimSpace(r.FormValue("name")),
		CommunityLists: splitList(r.FormValue("community_lists")),
		P2PDirect:      r.FormValue("p2p_direct") == "on",
		DirectRUZones:  r.FormValue("direct_ru_zones") == "on",
		DNSType:        r.FormValue("dns_type"),
		DNSServer:      strings.TrimSpace(r.FormValue("dns_server")),
		UpdateInterval: r.FormValue("update_interval"),
	}
	if p.Name == "" {
		s.adminDashboard(w, r, "Profile name is required")
		return
	}
	if err := s.store.PutProfile(p); err != nil {
		s.adminDashboard(w, r, "Failed to save profile: "+err.Error())
		return
	}
	s.adminDashboard(w, r, "Profile saved")
}

func (s *Server) adminDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		_ = s.store.DeleteProfile(r.FormValue("id"))
	}
	s.adminDashboard(w, r, "Profile deleted")
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.adminTmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("admin render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// ---- helpers ----

func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func sanitizeEmail(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "client"
	}
	return b.String()
}

func mustToken(n int) string {
	t, err := store.NewID(n)
	if err != nil {
		panic(err)
	}
	return t
}

// newUUID returns a random RFC 4122 v4 UUID string.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// publicBaseURL reconstructs the externally visible base URL for building sub links.
func publicBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host
}
