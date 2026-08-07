package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/nifigaprikolno/podkop/server/internal/store"
)

// dnsBlock is the DNS portion of a route profile.
type dnsBlock struct {
	Type   string `json:"type,omitempty"`
	Server string `json:"server,omitempty"`
}

// ProfileResponse is the JSON document a router fetches from /api/v1/profile.
// Its shape matches what the podkop client's remote_config.sh expects.
type ProfileResponse struct {
	ProxyString    string    `json:"proxy_string,omitempty"`
	CommunityLists []string  `json:"community_lists"`
	P2PDirect      bool      `json:"p2p_direct"`
	DirectRUZones  bool      `json:"direct_ru_zones"`
	DNS            *dnsBlock `json:"dns,omitempty"`
	UpdateInterval string    `json:"update_interval,omitempty"`
}

// AssembleProfile builds the profile document a client receives. It is pure so it
// can be unit tested without HTTP.
func AssembleProfile(c *store.Client, p *store.Profile) ProfileResponse {
	resp := ProfileResponse{
		ProxyString:    c.ProxyString,
		CommunityLists: p.CommunityLists,
		P2PDirect:      p.P2PDirect,
		DirectRUZones:  p.DirectRUZones,
		UpdateInterval: p.UpdateInterval,
	}
	if resp.CommunityLists == nil {
		resp.CommunityLists = []string{}
	}
	if p.DNSType != "" || p.DNSServer != "" {
		resp.DNS = &dnsBlock{Type: p.DNSType, Server: p.DNSServer}
	}
	return resp
}

// handleProfile serves GET /api/v1/profile. Auth is by client token supplied as
// ?token=, Authorization: Bearer <token>, or X-Podkop-Token.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	token := c.Token

	p, err := s.store.Profile(c.ProfileID)
	if err != nil {
		// Fall back to the default profile so a client is never left without one.
		p, err = s.store.EnsureDefaultProfile()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// A pull is the only liveness signal the panel gets from a router, so
	// record it before answering.
	ip := s.clientIP(r)
	if err := s.store.TouchClient(token, ip); err != nil {
		log.Printf("touch client: %v", err)
	}
	s.events.Info("PULL", fmt.Sprintf("%s fetched profile %q from %s", c.Name, p.Name, ip))

	resp := AssembleProfile(c, p)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// authenticateClient resolves the client token on a router-facing endpoint and
// writes the response itself when it cannot. An unknown token is answered with
// 404 rather than 403 so probing cannot tell valid tokens from invalid ones.
func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request) (*store.Client, bool) {
	token := extractToken(r)
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return nil, false
	}

	c, err := s.store.Client(token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return nil, false
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if !c.Enabled {
		http.Error(w, "client disabled", http.StatusForbidden)
		return nil, false
	}
	return c, true
}

// extractToken pulls the client token from the request.
func extractToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if t := r.Header.Get("X-Podkop-Token"); t != "" {
		return t
	}
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

// handleSubscription serves GET /api/v1/sub — the same key the router gets, in
// the shape phone clients expect. /api/v1/profile answers with podkop's own
// profile document, which a client like Hiddify or v2rayNG cannot parse at all:
// a subscription is a list of proxy URIs, conventionally base64 encoded.
//
// Auth is the client token, exactly as on /api/v1/profile.
func (s *Server) handleSubscription(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	if c.ProxyString == "" {
		http.Error(w, "no proxy configured for this client", http.StatusNotFound)
		return
	}

	ip := s.clientIP(r)
	if err := s.store.TouchClient(c.Token, ip); err != nil {
		log.Printf("touch client: %v", err)
	}
	s.events.Info("SUB", fmt.Sprintf("%s fetched a subscription from %s", c.Name, ip))

	// Some clients read the raw text and some insist on base64; base64 is what
	// the convention settled on and every client accepts it.
	body := base64.StdEncoding.EncodeToString([]byte(c.ProxyString + "\n"))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Names the entry in the client's profile list instead of leaving it "unnamed".
	w.Header().Set("Profile-Title", c.Name)
	s.noIndex(w)
	_, _ = w.Write([]byte(body))
}
