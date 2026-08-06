package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

// stateResponse feeds the live regions of the panel (the overview tiles and the
// log tail) without reloading the page. It carries only what those two screens
// render, and only for an authenticated session — the auth gate in handleAdmin
// already covers it.
type stateResponse struct {
	Uptime string      `json:"uptime"`
	HUD    []hudTile   `json:"hud"`
	Meters []meterView `json:"meters"`
	Hours  []hourBar   `json:"hours"`
	Events []eventView `json:"events"`
	Logs   []eventView `json:"logs"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp := stateResponse{Uptime: s.newPage(screenOverview).Uptime}

	switch q.Get("screen") {
	case screenLogs:
		resp.Logs = eventViews(s.events.Entries(strings.ToUpper(q.Get("level")), 200))
	default:
		stats := s.host.Collect()
		resp.HUD = s.hudTiles(stats)
		resp.Meters = s.meterViews(stats)
		resp.Hours = s.hourBars()
		resp.Events = eventViews(s.events.Entries("", 12))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}
