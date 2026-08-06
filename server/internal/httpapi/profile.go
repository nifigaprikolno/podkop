package httpapi

import (
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
	token := extractToken(r)
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	c, err := s.store.Client(token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Do not distinguish unknown token from other errors to avoid leaking.
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !c.Enabled {
		http.Error(w, "client disabled", http.StatusForbidden)
		return
	}

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
