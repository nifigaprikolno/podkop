package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nifigaprikolno/podkop/server/internal/config"
	"github.com/nifigaprikolno/podkop/server/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	cfg := &config.Config{
		AdminPath:     "/manage-secret/",
		AdminUser:     "admin",
		AdminPassword: "s3cret",
	}
	srv, err := NewServer(cfg, st, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv, st
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

func TestDecoyDeadForm(t *testing.T) {
	srv, _ := newTestServer(t)
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
