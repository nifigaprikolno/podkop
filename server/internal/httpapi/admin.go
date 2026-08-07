package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/nifigaprikolno/podkop/server/internal/store"
	"github.com/nifigaprikolno/podkop/server/internal/xui"
)

const sessionCookie = "pk_admin"

// notices maps flash codes to operator-facing text. Redirects carry the code,
// never the text: nothing from the query string is ever rendered as-is.
var notices = map[string]string{
	"client_saved":    "Client saved",
	"client_deleted":  "Client deleted",
	"client_updated":  "Client updated",
	"profile_saved":   "Profile saved",
	"profile_deleted": "Profile deleted",
	"post_saved":      "Post saved",
	"post_deleted":    "Post deleted",
	"store_reloaded":  "Store reloaded from disk",
	"sessions_reset":  "All sessions dropped",
}

var errNotices = map[string]string{
	"name_required":    "Client name is required",
	"proxy_required":   "Provide a proxy link or issue one via 3x-UI",
	"xui_disabled":     "3x-UI integration is not configured",
	"xui_failed":       "3x-UI refused to issue the key — see the logs",
	"title_required":   "Post title is required",
	"profile_required": "Profile name is required",
	"save_failed":      "Saving failed — see the logs",
	"unknown_client":   "No such client",
}

// handleAdmin dispatches everything under the secret operator path.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	s.noIndex(w)

	// Off the hostnames the operator area is meant to answer on, behave as the
	// site does for any path it does not know. Answering differently — a 403, a
	// bare 404 — would confirm the secret path to anyone who guessed it.
	if !s.adminReachable(r) {
		s.siteNotFound(w)
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, s.cfg.AdminPath)
	rel = strings.TrimPrefix(rel, "/")

	// The DSEG7 font is needed by the login page too, so it sits before the
	// auth gate. It is a font file — there is nothing to leak.
	if strings.HasPrefix(rel, "assets/") {
		http.StripPrefix(strings.TrimSuffix(s.cfg.AdminPath, "/")+"/assets", s.assetHandler()).ServeHTTP(w, r)
		return
	}

	switch rel {
	case "", "login":
		s.adminLogin(w, r)
		return
	case "logout":
		s.adminLogout(w, r)
		return
	}

	// Everything below requires an authenticated session.
	sess := s.session(r)
	if sess == nil {
		http.Redirect(w, r, s.cfg.AdminPath, http.StatusSeeOther)
		return
	}

	// Mutations are POST-only and CSRF-checked. SameSite=Lax already blocks
	// most cross-site posts; the token covers the rest.
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(sess.csrf)) != 1 {
			s.events.Warn("AUTH", "rejected a POST with a bad CSRF token from "+s.clientIP(r))
			http.Error(w, "bad csrf token", http.StatusForbidden)
			return
		}
	}

	switch rel {
	case screenNews:
		s.adminNews(w, r)
	case "news/save":
		s.adminSavePost(w, r)
	case "news/delete":
		s.adminDeletePost(w, r)

	case screenOverview, screenClients, screenOutbounds, screenRouting, screenLogs, screenConfig:
		s.adminScreen(w, r, rel)
	case "dashboard":
		http.Redirect(w, r, s.cfg.AdminPath+screenOverview, http.StatusSeeOther)

	case "clients/save", "clients/create":
		s.adminSaveClient(w, r)
	case "clients/delete":
		s.adminDeleteClient(w, r)
	case "clients/toggle":
		s.adminToggleClient(w, r)

	case "profiles/save":
		s.adminSaveProfile(w, r)
	case "profiles/delete":
		s.adminDeleteProfile(w, r)

	case "outbounds/probe":
		s.adminProbeOutbound(w, r)

	case "config/reload-store":
		s.adminReloadStore(w, r)
	case "config/drop-sessions":
		s.adminDropSessions(w, r)
	case "config/check-xui":
		s.adminCheckXUI(w, r)

	case "api/state":
		s.handleState(w, r)

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) assetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.FileServer(http.FS(s.assetFS)).ServeHTTP(w, r)
	})
}

// ---- authentication ----

// session returns the live session for a request, refreshing its TTL, or nil.
func (s *Server) session(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[c.Value]
	if !ok {
		return nil
	}
	if time.Now().After(sess.expiresAt) {
		delete(s.sessions, c.Value)
		return nil
	}
	sess.expiresAt = time.Now().Add(s.cfg.SessionTTL)
	return sess
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	s.noIndex(w)
	if s.session(r) != nil {
		http.Redirect(w, r, s.cfg.AdminPath+screenOverview, http.StatusSeeOther)
		return
	}

	ip := s.clientIP(r)
	if locked, left := s.loginGuard.Locked(ip); locked {
		s.renderLogin(w, http.StatusTooManyRequests,
			fmt.Sprintf("Too many attempts. Try again in %s.", left.Round(time.Second)))
		return
	}

	if r.Method != http.MethodPost {
		s.renderLogin(w, http.StatusOK, "")
		return
	}

	user := r.FormValue("username")
	pass := r.FormValue("password")
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) == 1
	if !userOK || !passOK {
		locked := s.loginGuard.Fail(ip)
		s.events.Warn("AUTH", fmt.Sprintf("failed login for %q from %s", user, ip))
		if locked {
			s.events.Warn("AUTH", fmt.Sprintf("%s locked out for %s", ip, s.cfg.LoginLockout))
			s.renderLogin(w, http.StatusTooManyRequests,
				fmt.Sprintf("Too many attempts. Try again in %s.", s.cfg.LoginLockout))
			return
		}
		// Slow down guessing a little without holding the handler hostage.
		time.Sleep(400 * time.Millisecond)
		s.renderLogin(w, http.StatusUnauthorized, "Wrong credentials")
		return
	}

	s.loginGuard.Reset(ip)
	sid := mustToken(24)
	s.mu.Lock()
	s.sessions[sid] = &session{
		csrf:      mustToken(16),
		expiresAt: time.Now().Add(s.cfg.SessionTTL),
		ip:        ip,
	}
	s.mu.Unlock()
	s.events.Info("AUTH", "operator signed in from "+ip)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sid,
		Path:     "/", // the login form also lives on the public root
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, s.cfg.AdminPath+screenOverview, http.StatusSeeOther)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := pageData{AdminPath: s.cfg.AdminPath, Styles: s.adminCSS, Error: errMsg, Version: s.build.Version}
	if err := s.adminTmpl.ExecuteTemplate(w, "login", data); err != nil {
		log.Printf("admin render login: %v", err)
	}
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---- screens ----

func (s *Server) adminScreen(w http.ResponseWriter, r *http.Request, screen string) {
	q := r.URL.Query()
	p := s.newPage(screen)
	p.CSRF = s.csrfFor(r)
	p.Notice = notices[q.Get("notice")]
	p.Error = errNotices[q.Get("err")]
	p.Query = q.Get("q")
	p.Filter = strings.ToUpper(q.Get("filter"))
	p.LogLevel = strings.ToUpper(q.Get("level"))

	switch screen {
	case screenOverview:
		stats := s.host.Collect()
		p.HUD = s.hudTiles(stats)
		p.Meters = s.meterViews(stats)
		p.Sources = s.sourceRows()
		p.Hours = s.hourBars()
		p.Events = eventViews(s.events.Entries("", 12))

	case screenClients:
		base := s.publicBaseURL(r)
		p.Clients = s.clientViews(base, p.Query, p.Filter)
		p.Profiles = s.store.Profiles()
		if id := q.Get("edit"); id != "" {
			if c, err := s.store.Client(id); err == nil {
				v := clientView{
					Client:      c,
					MaskedToken: maskToken(c.Token),
					ProfileURL:  fmt.Sprintf("%s/api/v1/profile?token=%s", base, c.Token),
					SubURL:      fmt.Sprintf("%s/api/v1/sub?token=%s", base, c.Token),
				}
				p.EditClient = &v
			} else {
				p.Error = errNotices["unknown_client"]
			}
		}
		if token := q.Get("created"); token != "" {
			if c, err := s.store.Client(token); err == nil {
				v := clientView{
					Client:      c,
					SubURL:      fmt.Sprintf("%s/api/v1/profile?token=%s", base, c.Token),
					MaskedToken: maskToken(c.Token),
				}
				p.Created = &v
			}
		}

	case screenOutbounds:
		probe := map[string]string{}
		if endpoint, result := q.Get("probed"), q.Get("rtt"); endpoint != "" && result != "" {
			probe[endpoint] = result
		}
		p.Outbounds = s.outboundViews(probe)

	case screenRouting:
		p.Profiles = s.store.Profiles()
		p.Lists = s.listRows()
		if len(p.Profiles) > 0 {
			selected := p.Profiles[0]
			if id := q.Get("profile"); id != "" {
				if pr, err := s.store.Profile(id); err == nil {
					selected = pr
				}
			}
			p.Rules = routingRules(selected)
			p.Drift = s.driftFor(selected)
			p.Filter = selected.ID
		}

	case screenLogs:
		p.Logs = eventViews(s.events.Entries(p.LogLevel, 200))

	case screenConfig:
		p.Env = s.envRows()
		p.Build = s.buildRows()
	}

	s.render(w, "page", p)
}

func (s *Server) buildRows() []kvRow {
	stats := s.host.Collect()
	xuiState := "not configured"
	if s.xui != nil {
		xuiState = s.cfg.XUIBaseURL
	}
	return []kvRow{
		{Label: "PANEL", Value: orDash(s.build.Version)},
		{Label: "COMMIT", Value: orDash(s.build.Commit)},
		{Label: "BUILT", Value: orDash(s.build.Date)},
		{Label: "GO", Value: goVersion()},
		{Label: "PLATFORM", Value: platform()},
		{Label: "UPTIME", Value: formatUptime(stats.Uptime)},
		{Label: "3X-UI", Value: xuiState},
		{Label: "LICENSE", Value: "AGPL-3.0"},
	}
}

func (s *Server) csrfFor(r *http.Request) string {
	if sess := s.session(r); sess != nil {
		return sess.csrf
	}
	return ""
}

// ---- clients ----

func (s *Server) adminSaveClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.redirect(w, r, screenClients, "", "")
		return
	}

	token := r.FormValue("token")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.redirect(w, r, screenClients, "", "name_required")
		return
	}

	c := &store.Client{Enabled: true}
	if token != "" {
		existing, err := s.store.Client(token)
		if err != nil {
			s.redirect(w, r, screenClients, "", "unknown_client")
			return
		}
		c = existing
	}
	c.Name = name
	c.ProfileID = r.FormValue("profile_id")

	proxy := strings.TrimSpace(r.FormValue("proxy_string"))
	if r.FormValue("mode") == "xui" {
		if s.xui == nil {
			s.redirect(w, r, screenClients, "", "xui_disabled")
			return
		}
		link, email, err := s.issueXUIKey(r.Context(), name)
		if err != nil {
			log.Printf("issue xui key: %v", err)
			s.events.Error("XUI", "failed to issue a key: "+err.Error())
			s.redirect(w, r, screenClients, "", "xui_failed")
			return
		}
		proxy = link
		c.XUIEmail = email
	}
	if proxy != "" {
		c.ProxyString = proxy
	}
	if c.ProxyString == "" {
		s.redirect(w, r, screenClients, "", "proxy_required")
		return
	}

	isNew := token == ""
	if err := s.store.PutClient(c); err != nil {
		log.Printf("save client: %v", err)
		s.redirect(w, r, screenClients, "", "save_failed")
		return
	}

	if isNew {
		s.events.Info("CLIENT", fmt.Sprintf("issued a key for %q", c.Name))
		http.Redirect(w, r, s.cfg.AdminPath+screenClients+"?created="+c.Token, http.StatusSeeOther)
		return
	}
	s.events.Info("CLIENT", fmt.Sprintf("updated %q", c.Name))
	s.redirect(w, r, screenClients, "client_saved", "")
}

func (s *Server) adminDeleteClient(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		token := r.FormValue("token")
		if c, err := s.store.Client(token); err == nil {
			s.events.Info("CLIENT", fmt.Sprintf("deleted %q", c.Name))
		}
		_ = s.store.DeleteClient(token)
	}
	s.redirect(w, r, screenClients, "client_deleted", "")
}

func (s *Server) adminToggleClient(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if c, err := s.store.Client(r.FormValue("token")); err == nil {
			c.Enabled = !c.Enabled
			_ = s.store.PutClient(c)
			s.events.Info("CLIENT", fmt.Sprintf("%q is now %s", c.Name, boolLabel(c.Enabled, "enabled", "disabled")))
		}
	}
	s.redirect(w, r, screenClients, "client_updated", "")
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
	// Read the inbound first: its transport decides whether the client may
	// carry an XTLS flow, and the client has to be created with the same flow
	// the link advertises.
	in, err := s.xui.GetInbound(ctx, s.cfg.XUIInbound)
	if err != nil {
		return "", "", err
	}
	uuid, err := newUUID()
	if err != nil {
		return "", "", err
	}
	email = fmt.Sprintf("%s-%s", sanitizeEmail(name), mustToken(3))
	cl := xui.NewClientSettings(uuid, email)
	if s.cfg.XUIClientFlow != "" {
		if in.SupportsFlow() {
			cl.Flow = s.cfg.XUIClientFlow
		} else {
			network, _, _ := in.Network()
			s.events.Warn("XUI", fmt.Sprintf(
				"inbound %d uses %s, issuing the key without flow", s.cfg.XUIInbound, network))
		}
	}
	if err := s.xui.AddClient(ctx, s.cfg.XUIInbound, cl); err != nil {
		return "", "", err
	}
	link, err = s.xui.BuildVlessLink(in, cl)
	if err != nil {
		return "", "", err
	}
	return link, email, nil
}

// ---- profiles ----

func (s *Server) adminSaveProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.redirect(w, r, screenRouting, "", "")
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
		s.redirect(w, r, screenRouting, "", "profile_required")
		return
	}
	if err := s.store.PutProfile(p); err != nil {
		log.Printf("save profile: %v", err)
		s.redirect(w, r, screenRouting, "", "save_failed")
		return
	}
	s.events.Info("ROUTING", fmt.Sprintf("profile %q saved — routers pick it up on the next pull", p.Name))
	s.redirect(w, r, screenRouting, "profile_saved", "")
}

func (s *Server) adminDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		_ = s.store.DeleteProfile(r.FormValue("id"))
		s.events.Info("ROUTING", "profile deleted")
	}
	s.redirect(w, r, screenRouting, "profile_deleted", "")
}

// ---- outbounds ----

// adminProbeOutbound measures TCP reachability of an exit endpoint. It is a
// read-only check: the panel dials the address and reports the handshake time.
func (s *Server) adminProbeOutbound(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimSpace(r.FormValue("endpoint"))
	result := "unreachable"
	if endpoint != "" {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", endpoint, 3*time.Second)
		if err == nil {
			conn.Close()
			result = fmt.Sprintf("%d", time.Since(start).Milliseconds())
		}
		s.events.Info("PROBE", fmt.Sprintf("%s → %s", endpoint, result))
	}
	http.Redirect(w, r, fmt.Sprintf("%s%s?probed=%s&rtt=%s",
		s.cfg.AdminPath, screenOutbounds, url.QueryEscape(endpoint), url.QueryEscape(result)),
		http.StatusSeeOther)
}

// ---- config actions (read-only or session-scoped only) ----

func (s *Server) adminReloadStore(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Reload(); err != nil {
		log.Printf("reload store: %v", err)
		s.redirect(w, r, screenConfig, "", "save_failed")
		return
	}
	s.events.Info("STORE", "reloaded from disk")
	s.redirect(w, r, screenConfig, "store_reloaded", "")
}

func (s *Server) adminDropSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.sessions = map[string]*session{}
	s.mu.Unlock()
	s.events.Warn("AUTH", "all sessions dropped by the operator")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, s.cfg.AdminPath, http.StatusSeeOther)
}

func (s *Server) adminCheckXUI(w http.ResponseWriter, r *http.Request) {
	if s.xui == nil {
		s.redirect(w, r, screenConfig, "", "xui_disabled")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.xui.Login(ctx); err != nil {
		log.Printf("check xui: %v", err)
		s.events.Error("XUI", "check failed: "+err.Error())
		s.redirect(w, r, screenConfig, "", "xui_failed")
		return
	}
	s.events.Info("XUI", "connection check succeeded")
	s.redirect(w, r, screenConfig, "client_updated", "")
}

// ---- rendering helpers ----

// redirect implements POST → Redirect → GET so a refresh never repeats an
// action. Only fixed codes travel in the URL.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, screen, notice, errCode string) {
	target := s.cfg.AdminPath + screen
	switch {
	case notice != "":
		target += "?notice=" + notice
	case errCode != "":
		target += "?err=" + errCode
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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

func goVersion() string { return runtime.Version() }

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

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

// publicBaseURL is the base the router-facing links are built from. The
// configured value wins: the host the operator is browsing is frequently not
// the host machines can reach.
func (s *Server) publicBaseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	// Same rule as everywhere else: a forwarded header is only believed when
	// something trusted is known to be in front.
	if s.cfg.TrustedProxy {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = fh
		}
	}
	return scheme + "://" + host
}
