package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nifigaprikolno/podkop/server/internal/config"
	"github.com/nifigaprikolno/podkop/server/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	return newTestServerWith(t, func(*config.Config) {})
}

func newTestServerWith(t *testing.T, tweak func(*config.Config)) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	cfg := &config.Config{
		AdminPath:     "/manage-secret/",
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		Root:          "site",
		SiteName:      "Backfire",
		SiteTagline:   "an open-city street racer on s&box",
		SessionTTL:    time.Hour,
		LoginMaxFails: 3,
		LoginLockout:  15 * time.Minute,
	}
	tweak(cfg)
	srv, err := NewServer(cfg, st, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv, st
}

// login signs in and returns a client-side cookie jar plus the session's CSRF
// token, so tests can exercise the authenticated screens and their forms.
func login(t *testing.T, srv *Server) (*http.Cookie, string) {
	t.Helper()
	h := srv.Handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, srv.cfg.AdminPath+"login",
		strings.NewReader("username=admin&password=s3cret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("login: code = %d, want 303 (body %s)", rr.Code, rr.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login did not set a session cookie")
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	sess := srv.sessions[cookie.Value]
	if sess == nil {
		t.Fatal("session not registered")
	}
	return cookie, sess.csrf
}

// get performs an authenticated GET and returns the recorder.
func get(t *testing.T, srv *Server, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// post performs an authenticated form POST.
func post(t *testing.T, srv *Server, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func TestAssembleProfile(t *testing.T) {
	c := &store.Client{ProxyString: "vless://uuid@host:443", ProfileID: "p1"}
	p := &store.Profile{
		CommunityLists: []string{"russia_inside", "meta"},
		P2PDirect:      true,
		DirectRUZones:  true,
		DNSType:        "doh",
		DNSServer:      "https://dns.example/dns-query",
		UpdateInterval: "1d",
	}
	got := AssembleProfile(c, p)
	if got.ProxyString != c.ProxyString {
		t.Errorf("proxy_string = %q", got.ProxyString)
	}
	if len(got.CommunityLists) != 2 || got.CommunityLists[0] != "russia_inside" {
		t.Errorf("community_lists = %v", got.CommunityLists)
	}
	if !got.P2PDirect || !got.DirectRUZones {
		t.Errorf("flags not carried: %+v", got)
	}
	if got.DNS == nil || got.DNS.Type != "doh" || got.DNS.Server == "" {
		t.Errorf("dns block = %+v", got.DNS)
	}
	if got.UpdateInterval != "1d" {
		t.Errorf("update_interval = %q", got.UpdateInterval)
	}
}

func TestAssembleProfileEmptyCommunityLists(t *testing.T) {
	got := AssembleProfile(&store.Client{}, &store.Profile{})
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"community_lists":[]`) {
		t.Errorf("community_lists should serialize as [], got %s", b)
	}
}

func TestProfileEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	if _, err := st.EnsureDefaultProfile(); err != nil {
		t.Fatal(err)
	}
	c := &store.Client{Name: "router1", ProxyString: "vless://x@h:443", ProfileID: "default", Enabled: true}
	if err := st.PutClient(c); err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()

	// Missing token → 401.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no token: code = %d, want 401", rr.Code)
	}

	// Unknown token → 404.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/profile?token=nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("bad token: code = %d, want 404", rr.Code)
	}

	// Valid token → 200 + JSON.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/profile?token="+c.Token, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("valid token: code = %d, want 200", rr.Code)
	}
	var resp ProfileResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ProxyString != c.ProxyString {
		t.Errorf("proxy_string = %q", resp.ProxyString)
	}
	if len(resp.CommunityLists) == 0 || resp.CommunityLists[0] != "russia_inside" {
		t.Errorf("community_lists = %v", resp.CommunityLists)
	}

	// Disabled client → 403.
	c.Enabled = false
	if err := st.PutClient(c); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/profile?token="+c.Token, nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("disabled: code = %d, want 403", rr.Code)
	}
}

// The 4PDA camouflage is no longer the default root, but it stays available for
// deployments that keep the panel unpublished.
func TestDecoyDeadForm(t *testing.T) {
	srv, _ := newTestServerWith(t, func(c *config.Config) { c.Root = "decoy" })
	h := srv.Handler()

	// GET / → decoy login page.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /: code = %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "4</b>PDA") && !strings.Contains(string(body), "4PDA") {
		t.Errorf("decoy page does not look like the 4PDA login")
	}

	// POST to the fake login → always a plausible failure, never a redirect/auth.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/index.php?act=Login&CODE=01",
		strings.NewReader("ips_username=someone&ips_password=whatever"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("dead form: code = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Неверный логин") {
		t.Errorf("dead form should return a generic failure message")
	}
}

func TestAdminRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	// Dashboard without session → redirect to login.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/manage-secret/dashboard", nil))
	if rr.Code != http.StatusSeeOther {
		t.Errorf("unauthed dashboard: code = %d, want 303", rr.Code)
	}

	// Login page renders.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/manage-secret/", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "podkop-server") {
		t.Errorf("login page: code = %d", rr.Code)
	}
}

// ---- public site ----

func TestSiteShowsPublishedPostsOnly(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.PutPost(&store.Post{Title: "Gearbox rewrite", Summary: "Torque curve", Body: "# Shift\ndone", Published: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPost(&store.Post{Title: "Secret plans", Body: "not ready", Published: false}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /: code = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Gearbox rewrite") {
		t.Errorf("published post missing from the feed")
	}
	if strings.Contains(body, "Secret plans") {
		t.Errorf("draft leaked onto the public site")
	}
	if !strings.Contains(body, "Backfire") {
		t.Errorf("site chrome missing — this is the cover story, it has to look like a real site")
	}
}

// The brand comes from configuration: the site is only a believable cover while
// its name matches the domain it is served from.
func TestSiteBrandComesFromConfig(t *testing.T) {
	srv, _ := newTestServerWith(t, func(c *config.Config) {
		c.SiteName = "Nitro"
		c.SiteTagline = "a demolition derby on s&box"
	})

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()

	for _, want := range []string{"Nitro", "a demolition derby on s&amp;box", "<title>Nitro — "} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "Backfire") {
		t.Errorf("default brand leaked into a configured site")
	}
}

func TestSitePostPageAndDraft404(t *testing.T) {
	srv, st := newTestServer(t)
	live := &store.Post{Title: "Race HUD", Body: "Speed on a **seven-segment** readout.", Published: true}
	draft := &store.Post{Title: "Draft entry", Body: "wip", Published: false}
	if err := st.PutPost(live); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPost(draft); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/post/"+live.Slug, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("post page: code = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "<strong>seven-segment</strong>") {
		t.Errorf("markdown was not rendered: %s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/post/"+draft.Slug, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("draft page: code = %d, want 404", rr.Code)
	}
}

func TestRobotsAndNoIndex(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if !strings.Contains(rr.Body.String(), "Disallow: /") {
		t.Errorf("robots.txt = %q, want everything disallowed by default", rr.Body.String())
	}

	// With indexing switched on the site opens up — but robots.txt must never
	// name the operator path: the file is public, and a Disallow line would
	// hand the secret path to anyone who reads it.
	srv, _ = newTestServerWith(t, func(c *config.Config) { c.SiteIndexing = true })
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	body := rr.Body.String()
	if !strings.Contains(body, "Allow: /") {
		t.Errorf("robots.txt = %q, want the site allowed", body)
	}
	if strings.Contains(body, "manage-secret") {
		t.Errorf("robots.txt leaks the admin path: %q", body)
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/manage-secret/", nil))
	if got := rr.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag = %q, want noindex on the operator area", got)
	}
}

// ---- operator screens ----

func TestAllScreensRender(t *testing.T) {
	srv, st := newTestServer(t)
	if _, err := st.EnsureDefaultProfile(); err != nil {
		t.Fatal(err)
	}
	if err := st.PutClient(&store.Client{
		Name: "home-router", ProxyString: "vless://uuid@vpn.example:443", ProfileID: "default", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPost(&store.Post{Title: "First entry", Published: true}); err != nil {
		t.Fatal(err)
	}
	cookie, _ := login(t, srv)

	for _, screen := range []string{"news", "overview", "clients", "outbounds", "routing", "logs", "config"} {
		rr := get(t, srv, cookie, "/manage-secret/"+screen)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200", screen, rr.Code)
			continue
		}
		body := rr.Body.String()
		if !strings.Contains(body, "PODKOP") {
			t.Errorf("%s: chrome missing", screen)
		}
		switch screen {
		case "clients":
			for _, want := range []string{"home-router", "Default (Russia)"} {
				if !strings.Contains(body, want) {
					t.Errorf("clients screen missing %q", want)
				}
			}
		case "outbounds":
			if !strings.Contains(body, "vpn.example:443") {
				t.Errorf("outbounds screen did not derive the endpoint from the issued key")
			}
		case "routing":
			if !strings.Contains(body, "russia_inside") {
				t.Errorf("routing screen missing the profile's community list")
			}
		case "news":
			if !strings.Contains(body, "First entry") {
				t.Errorf("news screen missing the post")
			}
		}
	}

	// The legacy dashboard path keeps working.
	if rr := get(t, srv, cookie, "/manage-secret/dashboard"); rr.Code != http.StatusSeeOther {
		t.Errorf("dashboard: code = %d, want a redirect to overview", rr.Code)
	}
}

func TestClientLifecycleUsesPostRedirectGet(t *testing.T) {
	srv, st := newTestServer(t)
	if _, err := st.EnsureDefaultProfile(); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, srv)

	rr := post(t, srv, cookie, "/manage-secret/clients/save", url.Values{
		"csrf":         {csrf},
		"name":         {"kitchen-router"},
		"profile_id":   {"default"},
		"mode":         {"manual"},
		"proxy_string": {"vless://uuid@vpn.example:443"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: code = %d, want 303", rr.Code)
	}
	location := rr.Header().Get("Location")
	if !strings.Contains(location, "created=") {
		t.Fatalf("create redirect = %q, want the issued token", location)
	}

	// Following the redirect shows the token exactly once, on the clients screen.
	rr = get(t, srv, cookie, location)
	clients := st.Clients()
	if len(clients) != 1 {
		t.Fatalf("store holds %d clients, want 1", len(clients))
	}
	if !strings.Contains(rr.Body.String(), clients[0].Token) {
		t.Errorf("issued token is not shown after the redirect")
	}

	// Rename through the same endpoint.
	rr = post(t, srv, cookie, "/manage-secret/clients/save", url.Values{
		"csrf":       {csrf},
		"token":      {clients[0].Token},
		"name":       {"renamed"},
		"profile_id": {"default"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("rename: code = %d, want 303", rr.Code)
	}
	updated, err := st.Client(clients[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" {
		t.Errorf("name = %q, want renamed", updated.Name)
	}
	if updated.ProxyString == "" {
		t.Errorf("an empty proxy field must keep the existing key, got it cleared")
	}

	// Toggle and delete.
	if rr := post(t, srv, cookie, "/manage-secret/clients/toggle", url.Values{
		"csrf": {csrf}, "token": {updated.Token},
	}); rr.Code != http.StatusSeeOther {
		t.Errorf("toggle: code = %d", rr.Code)
	}
	if c, _ := st.Client(updated.Token); c.Enabled {
		t.Errorf("toggle did not disable the client")
	}
	if rr := post(t, srv, cookie, "/manage-secret/clients/delete", url.Values{
		"csrf": {csrf}, "token": {updated.Token},
	}); rr.Code != http.StatusSeeOther {
		t.Errorf("delete: code = %d", rr.Code)
	}
	if len(st.Clients()) != 0 {
		t.Errorf("client survived deletion")
	}
}

func TestClientsSearchAndFilter(t *testing.T) {
	srv, st := newTestServer(t)
	if _, err := st.EnsureDefaultProfile(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*store.Client{
		{Name: "alpha", ProfileID: "default", Enabled: true, ProxyString: "vless://a@one.example:443"},
		{Name: "beta", ProfileID: "default", Enabled: false, ProxyString: "vless://b@two.example:443"},
	} {
		if err := st.PutClient(c); err != nil {
			t.Fatal(err)
		}
	}
	cookie, _ := login(t, srv)

	rr := get(t, srv, cookie, "/manage-secret/clients?q=alpha")
	if body := rr.Body.String(); !strings.Contains(body, "alpha") || strings.Contains(body, ">beta<") {
		t.Errorf("search did not narrow the list")
	}

	rr = get(t, srv, cookie, "/manage-secret/clients?filter=PAUSED")
	if body := rr.Body.String(); !strings.Contains(body, "beta") || strings.Contains(body, ">alpha<") {
		t.Errorf("PAUSED filter did not narrow the list")
	}
}

func TestPostEditingThroughTheCMS(t *testing.T) {
	srv, st := newTestServer(t)
	cookie, csrf := login(t, srv)

	rr := post(t, srv, cookie, "/manage-secret/news/save", url.Values{
		"csrf":      {csrf},
		"title":     {"Гараж и подвеска"},
		"summary":   {"lift works"},
		"body":      {"# Heading\ntext"},
		"published": {"on"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("save post: code = %d", rr.Code)
	}
	posts := st.Posts(false)
	if len(posts) != 1 {
		t.Fatalf("store holds %d posts, want 1", len(posts))
	}
	if posts[0].Slug != "garazh-i-podveska" {
		t.Errorf("slug = %q, want a transliterated slug", posts[0].Slug)
	}

	if rr := post(t, srv, cookie, "/manage-secret/news/delete", url.Values{
		"csrf": {csrf}, "id": {posts[0].ID},
	}); rr.Code != http.StatusSeeOther {
		t.Errorf("delete post: code = %d", rr.Code)
	}
	if len(st.Posts(false)) != 0 {
		t.Errorf("post survived deletion")
	}
}

// ---- security ----

func TestPostWithoutCSRFIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	cookie, _ := login(t, srv)

	rr := post(t, srv, cookie, "/manage-secret/clients/save", url.Values{
		"name": {"no-token"}, "proxy_string": {"vless://x@h:443"},
	})
	if rr.Code != http.StatusForbidden {
		t.Errorf("missing csrf: code = %d, want 403", rr.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	srv, _ := newTestServerWith(t, func(c *config.Config) { c.LoginMaxFails = 2 })
	h := srv.Handler()

	attempt := func(password string) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/manage-secret/login",
			strings.NewReader("username=admin&password="+password))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.7:5555"
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := attempt("wrong"); code != http.StatusUnauthorized {
		t.Errorf("first failure: code = %d, want 401", code)
	}
	if code := attempt("wrong"); code != http.StatusTooManyRequests {
		t.Errorf("second failure should trip the lockout, got %d", code)
	}
	// Even the right password is refused while the address is locked out.
	if code := attempt("s3cret"); code != http.StatusTooManyRequests {
		t.Errorf("locked out address: code = %d, want 429", code)
	}
}

// A spoofed forwarded header must not decide who gets locked out unless the
// deployment declares it sits behind a trusted proxy.
func TestLockoutKeyIgnoresUntrustedForwardedFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
		want    string
	}{
		{"direct", false, "203.0.113.7"},
		{"behind proxy", true, "198.51.100.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServerWith(t, func(c *config.Config) { c.TrustedProxy = tc.trusted })
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "203.0.113.7:5555"
			req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.1")
			if got := srv.clientIP(req); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	srv, _ := newTestServerWith(t, func(c *config.Config) { c.SessionTTL = time.Millisecond })
	cookie, _ := login(t, srv)
	time.Sleep(5 * time.Millisecond)

	if rr := get(t, srv, cookie, "/manage-secret/overview"); rr.Code != http.StatusSeeOther {
		t.Errorf("expired session: code = %d, want a redirect to login", rr.Code)
	}
}

func TestDropSessionsSignsEveryoneOut(t *testing.T) {
	srv, _ := newTestServer(t)
	cookie, csrf := login(t, srv)

	if rr := post(t, srv, cookie, "/manage-secret/config/drop-sessions", url.Values{"csrf": {csrf}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("drop sessions: code = %d", rr.Code)
	}
	if rr := get(t, srv, cookie, "/manage-secret/overview"); rr.Code != http.StatusSeeOther {
		t.Errorf("session survived the drop: code = %d", rr.Code)
	}
}

// ---- pull telemetry ----

// A profile pull is the only signal the panel gets from a router, so it has to
// be recorded — and a profile saved afterwards has to show up as drift.
func TestProfilePullIsRecordedAndDrivesDrift(t *testing.T) {
	srv, st := newTestServer(t)
	profile, err := st.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	c := &store.Client{Name: "router1", ProxyString: "vless://x@h:443", ProfileID: profile.ID, Enabled: true}
	if err := st.PutClient(c); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile?token="+c.Token, nil)
	req.RemoteAddr = "198.51.100.4:44444"
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	pulled, err := st.Client(c.Token)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.PullCount != 1 || pulled.LastSeen.IsZero() {
		t.Fatalf("pull not recorded: %+v", pulled)
	}
	if pulled.LastIP != "198.51.100.4" {
		t.Errorf("LastIP = %q, want the request address", pulled.LastIP)
	}
	if state, _ := clientState(pulled, profile); state != "active" {
		t.Errorf("state right after a pull = %q, want active", state)
	}

	// Saving the profile moves its revision past the client's last pull.
	if err := st.PutProfile(profile); err != nil {
		t.Fatal(err)
	}
	updated, err := st.Profile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, _ := clientState(pulled, updated); state != "drift" {
		t.Errorf("state after a profile change = %q, want drift", state)
	}

	cookie, _ := login(t, srv)
	if body := get(t, srv, cookie, "/manage-secret/routing").Body.String(); !strings.Contains(body, "PROFILE DRIFT") {
		t.Errorf("routing screen does not report the drift")
	}
}
