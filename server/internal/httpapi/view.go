package httpapi

import (
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nifigaprikolno/podkop/server/internal/eventlog"
	"github.com/nifigaprikolno/podkop/server/internal/hoststat"
	"github.com/nifigaprikolno/podkop/server/internal/store"
)

// pageData is what every operator template receives. One struct for all screens
// keeps the layout (nav, top bar, notices) written once.
type pageData struct {
	AdminPath string
	Section   string // "news" or "extras"
	Screen    string
	Title     string
	Subtitle  string
	Styles    template.CSS
	CSRF      string
	Notice    string
	Error     string
	Nav       []navItem
	Uptime    string
	Version   string

	Query    string
	Filter   string
	LogLevel string

	XUIEnabled bool

	// Screen payloads. Only the ones a screen needs are filled.
	HUD       []hudTile
	Meters    []meterView
	Sources   []sourceRow
	Events    []eventView
	Hours     []hourBar
	Clients   []clientView
	Profiles  []*store.Profile
	Outbounds []outboundView
	Rules     []ruleView
	Lists     []listRow
	Drift     driftView
	Logs      []eventView
	Env       []kvRow
	Build     []kvRow
	Posts     []postRow

	EditPost   *store.Post
	EditClient *clientView
	Created    *clientView
}

type navItem struct {
	Num     string
	Label   string
	Href    string
	Tag     string
	Active  bool
	Section string
}

// The view types double as the JSON payload of the live-refresh endpoint, so
// they carry explicit lowercase tags the page script can rely on.
type hudTile struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
	Note  string `json:"note"`
	Tone  string `json:"tone"` // cyan | amber | green | red | dim
}

type meterView struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Unit    string `json:"unit"`
	Percent int    `json:"percent"`
	Note    string `json:"note"`
	Tone    string `json:"tone"`
}

type sourceRow struct {
	Name  string
	Note  string
	Value string
	Tone  string
}

type eventView struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Tag   string `json:"tag"`
	Text  string `json:"text"`
}

type hourBar struct {
	Label   string `json:"label"`
	Percent int    `json:"percent"`
	Count   int    `json:"count"`
}

// clientView decorates a stored client with everything the table shows.
type clientView struct {
	*store.Client
	ProfileName string
	ProfileURL  string
	SubURL      string
	MaskedToken string
	Added       string
	LastSeenAgo string
	State       string // active | idle | drift | paused
	StateLabel  string
	Endpoint    string
	Source      string
}

type outboundView struct {
	Name     string
	Scheme   string
	Endpoint string
	Clients  int
	Source   string
	Probe    string
	Tone     string
}

type ruleView struct {
	Num    string
	Match  string
	Detail string
	Action string
	Tone   string
}

type listRow struct {
	Name  string
	Note  string
	Count string
}

type driftView struct {
	Total   int
	Behind  int
	Updated string
	Names   []string
}

type kvRow struct {
	Label string
	Value string
	Tone  string
}

type postRow struct {
	*store.Post
	Date   string
	Status string
	URL    string
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"join": strings.Join,
	}
}

// ---- builders ----

const (
	screenOverview  = "overview"
	screenClients   = "clients"
	screenOutbounds = "outbounds"
	screenRouting   = "routing"
	screenLogs      = "logs"
	screenConfig    = "config"
	screenNews      = "news"
)

var screenMeta = map[string][3]string{
	screenNews:      {"NEWS", "devlog posts of the public site", "news"},
	screenOverview:  {"OVERVIEW", "clients, host and panel state", "extras"},
	screenClients:   {"CLIENTS", "routers pulling their profile", "extras"},
	screenOutbounds: {"OUTBOUNDS", "exit paths handed to routers", "extras"},
	screenRouting:   {"ROUTING", "profiles pushed on the next pull", "extras"},
	screenLogs:      {"LOGS", "podkop-server, in memory since start", "extras"},
	screenConfig:    {"CONFIG", "environment and build, read-only", "extras"},
}

func (s *Server) newPage(screen string) pageData {
	meta := screenMeta[screen]
	p := pageData{
		AdminPath:  s.cfg.AdminPath,
		Screen:     screen,
		Section:    meta[2],
		Title:      meta[0],
		Subtitle:   meta[1],
		Styles:     s.adminCSS,
		XUIEnabled: s.xui != nil,
		Version:    s.build.Version,
	}
	p.Nav = s.navFor(screen)
	p.Uptime = formatUptime(time.Since(s.startedAt))
	return p
}

func (s *Server) navFor(active string) []navItem {
	order := []string{screenNews, screenOverview, screenClients, screenOutbounds, screenRouting, screenLogs, screenConfig}
	out := make([]navItem, 0, len(order))
	for i, key := range order {
		meta := screenMeta[key]
		item := navItem{
			Num:     fmt.Sprintf("%02d", i+1),
			Label:   meta[0],
			Href:    s.cfg.AdminPath + key,
			Active:  key == active,
			Section: meta[2],
		}
		switch key {
		case screenNews:
			if n := len(s.store.Posts(false)); n > 0 {
				item.Tag = fmt.Sprintf("%d", n)
			}
		case screenClients:
			if n := len(s.store.Clients()); n > 0 {
				item.Tag = fmt.Sprintf("%d", n)
			}
		}
		out = append(out, item)
	}
	return out
}

// clientViews decorates every client and applies the search/filter controls.
func (s *Server) clientViews(base, query, filter string) []clientView {
	profiles := s.store.Profiles()
	byID := map[string]*store.Profile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}

	clients := s.store.Clients()
	out := make([]clientView, 0, len(clients))
	for _, c := range clients {
		v := clientView{
			Client:      c,
			ProfileURL:  fmt.Sprintf("%s/api/v1/profile?token=%s", base, c.Token),
			SubURL:      fmt.Sprintf("%s/api/v1/sub?token=%s", base, c.Token),
			MaskedToken: maskToken(c.Token),
			Added:       formatDate(c.CreatedAt),
			LastSeenAgo: formatAgo(c.LastSeen),
			Source:      "manual",
		}
		if c.XUIEmail != "" {
			v.Source = "3x-ui"
		}
		if p := byID[c.ProfileID]; p != nil {
			v.ProfileName = p.Name
		}
		v.Endpoint = endpointOf(c.ProxyString)
		v.State, v.StateLabel = clientState(c, byID[c.ProfileID])

		if !matchesQuery(v, query) || !matchesFilter(v, filter) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func matchesQuery(v clientView, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, field := range []string{v.Name, v.Token, v.ProfileName, v.XUIEmail, v.Endpoint} {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func matchesFilter(v clientView, filter string) bool {
	switch strings.ToUpper(filter) {
	case "", "ALL":
		return true
	case "ONLINE":
		return v.State == "active"
	case "DRIFT":
		return v.State == "drift"
	case "PAUSED":
		return v.State == "paused"
	default:
		return true
	}
}

// clientState derives the badge shown in the table. "drift" means the router
// still runs an older revision: the profile changed after its last pull.
func clientState(c *store.Client, p *store.Profile) (string, string) {
	if !c.Enabled {
		return "paused", "PAUSED"
	}
	if c.LastSeen.IsZero() {
		return "idle", "NEVER"
	}
	if p != nil && !p.UpdatedAt.IsZero() && c.LastSeen.Before(p.UpdatedAt) {
		return "drift", "DRIFT"
	}
	if time.Since(c.LastSeen) > 2*pullInterval(p) {
		return "idle", "IDLE"
	}
	return "active", "ACTIVE"
}

// pullInterval converts a profile's update_interval into a duration. Unknown or
// missing values fall back to a day, which is the client default.
func pullInterval(p *store.Profile) time.Duration {
	if p == nil {
		return 24 * time.Hour
	}
	switch p.UpdateInterval {
	case "1h":
		return time.Hour
	case "3h":
		return 3 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "3d":
		return 72 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// outboundViews groups clients by the exit path their key points at. The panel
// has no node inventory of its own — the keys it handed out are the inventory.
func (s *Server) outboundViews(probe map[string]string) []outboundView {
	byEndpoint := map[string]*outboundView{}
	for _, c := range s.store.Clients() {
		endpoint := endpointOf(c.ProxyString)
		if endpoint == "" {
			continue
		}
		v := byEndpoint[endpoint]
		if v == nil {
			v = &outboundView{
				Name:     endpoint,
				Scheme:   strings.ToUpper(schemeOf(c.ProxyString)),
				Endpoint: endpoint,
				Source:   "manual",
				Tone:     "cyan",
			}
			byEndpoint[endpoint] = v
		}
		if c.XUIEmail != "" {
			v.Source = "3x-ui"
		}
		v.Clients++
	}

	out := make([]outboundView, 0, len(byEndpoint))
	for endpoint, v := range byEndpoint {
		v.Probe = probe[endpoint]
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Clients != out[j].Clients {
			return out[i].Clients > out[j].Clients
		}
		return out[i].Endpoint < out[j].Endpoint
	})
	return out
}

// routingRules renders a profile as the rule list a router will end up with.
func routingRules(p *store.Profile) []ruleView {
	if p == nil {
		return nil
	}
	var rules []ruleView
	add := func(match, detail, action, tone string) {
		rules = append(rules, ruleView{
			Num:    fmt.Sprintf("%02d", len(rules)+1),
			Match:  match,
			Detail: detail,
			Action: action,
			Tone:   tone,
		})
	}
	if p.P2PDirect {
		add("P2P / BitTorrent", "torrent trackers and peer traffic", "DIRECT", "green")
	}
	if p.DirectRUZones {
		add(".ru · .su · .рф", "russian zones bypass the tunnel", "DIRECT", "green")
	}
	for _, list := range p.CommunityLists {
		add("list "+list, "community list pushed to the router", "PROXY", "cyan")
	}
	if p.DNSType != "" || p.DNSServer != "" {
		add("DNS", strings.TrimSpace(p.DNSType+" "+p.DNSServer), "RESOLVE", "cyan")
	}
	add("everything else", "no rule matched", "DIRECT", "dim")
	return rules
}

// driftFor reports how many routers still run an older revision of a profile.
func (s *Server) driftFor(p *store.Profile) driftView {
	d := driftView{Updated: formatAgo(p.UpdatedAt)}
	for _, c := range s.store.Clients() {
		if c.ProfileID != p.ID || !c.Enabled {
			continue
		}
		d.Total++
		if c.LastSeen.Before(p.UpdatedAt) {
			d.Behind++
			if len(d.Names) < 6 {
				d.Names = append(d.Names, c.Name)
			}
		}
	}
	return d
}

func (s *Server) listRows() []listRow {
	counts := map[string]int{}
	for _, p := range s.store.Profiles() {
		for _, l := range p.CommunityLists {
			counts[l]++
		}
	}
	out := make([]listRow, 0, len(counts))
	for name, n := range counts {
		out = append(out, listRow{
			Name:  name,
			Note:  "community list",
			Count: fmt.Sprintf("%d", n),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// hudTiles are the four big readouts on the overview screen. Everything shown
// is measured by this server — there is no traffic accounting to draw from.
func (s *Server) hudTiles(stats hoststat.Stats) []hudTile {
	clients := s.store.Clients()
	enabled, drift := 0, 0
	profiles := s.store.Profiles()
	byID := map[string]*store.Profile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	for _, c := range clients {
		if c.Enabled {
			enabled++
		}
		if state, _ := clientState(c, byID[c.ProfileID]); state == "drift" {
			drift++
		}
	}

	pulls := 0
	for _, e := range s.events.Entries("", 0) {
		if e.Tag == "PULL" && time.Since(e.Time) < 24*time.Hour {
			pulls++
		}
	}

	driftNote := "all synced"
	driftTone := "green"
	if drift > 0 {
		driftNote = fmt.Sprintf("%d awaiting the next pull", drift)
		driftTone = "amber"
	}

	return []hudTile{
		{Label: "DEVICES ENABLED", Value: fmt.Sprintf("%d", enabled), Unit: fmt.Sprintf("/ %d", len(clients)), Note: driftNote, Tone: driftTone},
		{Label: "PROFILES", Value: fmt.Sprintf("%d", len(profiles)), Unit: "sets", Note: fmt.Sprintf("%d community lists in use", len(s.listRows())), Tone: "cyan"},
		{Label: "PULLS 24H", Value: fmt.Sprintf("%d", pulls), Unit: "requests", Note: "since the process started", Tone: "cyan"},
		{Label: "UPTIME", Value: formatUptime(stats.Uptime), Unit: "d.hh:mm", Note: "panel process", Tone: "green"},
	}
}

func (s *Server) meterViews(stats hoststat.Stats) []meterView {
	out := []meterView{}
	if stats.CPUValid {
		out = append(out, meterView{
			Label: "CPU", Value: fmt.Sprintf("%.0f", stats.CPUPercent), Unit: "%",
			Percent: clampPercent(stats.CPUPercent), Note: "host, since the last sample", Tone: tone(stats.CPUPercent, 70, 90),
		})
	}
	if stats.MemValid {
		pct := stats.MemUsedMB / stats.MemTotalMB * 100
		out = append(out, meterView{
			Label: "MEMORY", Value: fmt.Sprintf("%.1f", stats.MemUsedMB/1024), Unit: fmt.Sprintf("GB / %.0f", stats.MemTotalMB/1024),
			Percent: clampPercent(pct), Note: fmt.Sprintf("panel rss %.0f MB", stats.RSSMB), Tone: tone(pct, 75, 90),
		})
	}
	if stats.DiskValid {
		pct := stats.DiskUsedGB / stats.DiskTotalGB * 100
		out = append(out, meterView{
			Label: "DISK", Value: fmt.Sprintf("%.0f", pct), Unit: "%",
			Percent: clampPercent(pct), Note: fmt.Sprintf("%.0f GB free", stats.DiskTotalGB-stats.DiskUsedGB), Tone: tone(pct, 80, 92),
		})
	}
	out = append(out, meterView{
		Label: "GO RUNTIME", Value: fmt.Sprintf("%d", stats.Goroutines), Unit: "goroutines",
		Percent: clampPercent(float64(stats.Goroutines)), Note: fmt.Sprintf("heap %.1f MB", stats.HeapMB), Tone: "cyan",
	})
	return out
}

// hourBars count profile pulls per hour over the last day — the honest
// equivalent of the mockup's throughput graph.
func (s *Server) hourBars() []hourBar {
	const hours = 24
	counts := make([]int, hours)
	now := time.Now()
	for _, e := range s.events.Entries("", 0) {
		if e.Tag != "PULL" {
			continue
		}
		age := now.Sub(e.Time)
		if age < 0 || age >= hours*time.Hour {
			continue
		}
		counts[hours-1-int(age/time.Hour)]++
	}

	max := 1
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	out := make([]hourBar, hours)
	for i, c := range counts {
		out[i] = hourBar{
			Label:   fmt.Sprintf("%02d:00", now.Add(-time.Duration(hours-1-i)*time.Hour).Hour()),
			Percent: c * 100 / max,
			Count:   c,
		}
	}
	return out
}

func (s *Server) sourceRows() []sourceRow {
	rows := []sourceRow{
		{Name: "Public root", Note: "what visitors see on /", Value: strings.ToUpper(s.cfg.Root), Tone: "cyan"},
		{Name: "Store", Note: s.store.Path(), Value: fmt.Sprintf("%d clients", len(s.store.Clients())), Tone: "cyan"},
	}
	if s.xui != nil {
		rows = append(rows, sourceRow{Name: "3x-UI", Note: s.cfg.XUIBaseURL, Value: "CONFIGURED", Tone: "green"})
	} else {
		rows = append(rows, sourceRow{Name: "3x-UI", Note: "keys are pasted manually", Value: "OFF", Tone: "dim"})
	}
	rows = append(rows, sourceRow{
		Name:  "Login guard",
		Note:  fmt.Sprintf("%d fails → %s lockout", s.cfg.LoginMaxFails, s.cfg.LoginLockout),
		Value: boolLabel(s.cfg.TrustedProxy, "PROXY-AWARE", "DIRECT"),
		Tone:  "cyan",
	})
	return rows
}

func eventViews(entries []eventlog.Entry) []eventView {
	out := make([]eventView, 0, len(entries))
	for _, e := range entries {
		out = append(out, eventView{
			Time:  e.Time.Local().Format("15:04:05"),
			Level: e.Level,
			Tag:   e.Tag,
			Text:  e.Text,
		})
	}
	return out
}

func (s *Server) envRows() []kvRow {
	return []kvRow{
		{Label: "LISTEN", Value: s.cfg.Listen},
		{Label: "ROOT", Value: s.cfg.Root},
		{Label: "ADMIN PATH", Value: s.cfg.AdminPath},
		{Label: "ADMIN REACH", Value: adminReachLabel(s.cfg.AdminLocalOnly, s.cfg.AdminHost)},
		{Label: "ADMIN USER", Value: s.cfg.AdminUser},
		{Label: "ADMIN PASSWORD", Value: mask(s.cfg.AdminPassword)},
		{Label: "SESSION TTL", Value: s.cfg.SessionTTL.String()},
		{Label: "LOGIN LOCKOUT", Value: fmt.Sprintf("%d fails / %s", s.cfg.LoginMaxFails, s.cfg.LoginLockout)},
		{Label: "TRUSTED PROXY", Value: boolLabel(s.cfg.TrustedProxy, "yes", "no")},
		{Label: "SITE INDEXING", Value: boolLabel(s.cfg.SiteIndexing, "allowed", "disallowed")},
		{Label: "STORE", Value: s.store.Path()},
		{Label: "XUI BASE URL", Value: orDash(s.cfg.XUIBaseURL)},
		{Label: "XUI USER", Value: orDash(s.cfg.XUIUsername)},
		{Label: "XUI PASSWORD", Value: mask(s.cfg.XUIPassword)},
		{Label: "XUI INBOUND", Value: fmt.Sprintf("%d", s.cfg.XUIInbound)},
		{Label: "XUI PUBLIC HOST", Value: orDash(s.cfg.XUIPublicHost)},
	}
}

func (s *Server) postRows() []postRow {
	posts := s.store.Posts(false)
	out := make([]postRow, 0, len(posts))
	for _, p := range posts {
		row := postRow{Post: p, Date: formatDate(p.CreatedAt), Status: "DRAFT", URL: "/post/" + p.Slug}
		if p.Published {
			row.Status = "LIVE"
		}
		out = append(out, row)
	}
	return out
}

// ---- small helpers ----

func maskToken(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:6] + "…" + t[len(t)-4:]
}

func mask(v string) string {
	if v == "" {
		return "—"
	}
	return "•••• set"
}

func orDash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}

func boolLabel(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

// adminReachLabel spells out where the operator area answers, because "which
// hostnames can reach this login form" is the one config value worth being able
// to read off the screen rather than infer from two separate rows.
func adminReachLabel(localOnly bool, adminHost string) string {
	if !localOnly {
		return "ANY HOSTNAME"
	}
	if adminHost != "" {
		return "LOOPBACK + " + strings.ToUpper(adminHost)
	}
	return "LOOPBACK ONLY"
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("02.01.2006")
}

func formatAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%d.%02d:%02d", days, hours, mins)
}

func clampPercent(v float64) int {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return int(v)
	}
}

func tone(v, warn, crit float64) string {
	switch {
	case v >= crit:
		return "red"
	case v >= warn:
		return "amber"
	default:
		return "green"
	}
}

// schemeOf / endpointOf pull the transport and host:port out of a proxy link.
func schemeOf(link string) string {
	scheme, _, found := strings.Cut(link, "://")
	if !found {
		return "?"
	}
	return scheme
}

func endpointOf(link string) string {
	if link == "" {
		return ""
	}
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		return u.Host
	}
	// Shadowsocks links may carry base64 userinfo that url.Parse rejects.
	_, rest, found := strings.Cut(link, "://")
	if !found {
		return ""
	}
	if _, after, ok := strings.Cut(rest, "@"); ok {
		rest = after
	}
	rest, _, _ = strings.Cut(rest, "/")
	rest, _, _ = strings.Cut(rest, "?")
	rest, _, _ = strings.Cut(rest, "#")
	return rest
}
