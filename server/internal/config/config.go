// Package config loads podkop-server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration.
type Config struct {
	// Listen is the HTTP listen address, e.g. ":8080".
	Listen string
	// StorePath is the JSON store file path.
	StorePath string

	// AdminPath is the secret base path for the admin panel, e.g. "/manage-ab12cd/".
	// It MUST start and end with a slash. Keeping it secret is what hides the panel
	// behind the decoy page from active probing.
	AdminPath string
	// AdminUser / AdminPassword authenticate the operator on the admin panel.
	AdminUser     string
	AdminPassword string

	// DecoyDir optionally overrides the built-in decoy site with a custom one.
	DecoyDir string

	// Root selects what the public root serves: the Halogen devlog site
	// ("site", default), the 4PDA camouflage page ("decoy") or the operator
	// login form ("login").
	Root string
	// SiteIndexing opens robots.txt for the public site. The admin panel is
	// never indexable regardless of this flag.
	SiteIndexing bool
	// SiteName and SiteTagline brand the public site. They are configuration
	// rather than constants because the site is what visitors see: it has to
	// match whatever domain it is served from.
	SiteName    string
	SiteTagline string

	// SessionTTL bounds how long an operator session stays valid.
	SessionTTL time.Duration
	// LoginMaxFails / LoginLockout throttle password guessing per client IP.
	LoginMaxFails int
	LoginLockout  time.Duration
	// TrustedProxy makes the server believe X-Forwarded-For / CF-Connecting-IP.
	// Required behind nginx or a Cloudflare tunnel, harmful when the panel is
	// exposed directly: a spoofed header would let an attacker dodge the login
	// throttle or lock out someone else's address.
	TrustedProxy bool

	// 3x-UI integration (optional). When XUIBaseURL is empty, key issuance falls
	// back to manual proxy links pasted by the operator.
	XUIBaseURL    string
	XUIUsername   string
	XUIPassword   string
	XUIInbound    int    // inbound id on which new clients are created
	XUIPublicHost string // public host/IP to put into generated vless links
	// XUIClientFlow is the XTLS flow new clients are created with, e.g.
	// "xtls-rprx-vision". Applied only on TCP inbounds — Vision does not exist
	// on gRPC or WebSocket, and a link carrying flow there does not connect.
	XUIClientFlow string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(env(key, ""))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(env(key, ""))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid %s: must be positive", key)
	}
	return d, nil
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(env(key, ""))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid %s: expected a positive integer", key)
	}
	return n, nil
}

// Load reads the configuration from the environment and validates it.
func Load() (*Config, error) {
	c := &Config{
		Listen:        env("PODKOP_SERVER_LISTEN", ":8080"),
		StorePath:     env("PODKOP_SERVER_STORE", "/var/lib/podkop-server/store.json"),
		AdminPath:     env("PODKOP_SERVER_ADMIN_PATH", "/manage/"),
		AdminUser:     env("PODKOP_SERVER_ADMIN_USER", "admin"),
		AdminPassword: env("PODKOP_SERVER_ADMIN_PASSWORD", ""),
		DecoyDir:      env("PODKOP_SERVER_DECOY_DIR", ""),
		Root:          strings.ToLower(strings.TrimSpace(env("PODKOP_SERVER_ROOT", "site"))),
		SiteIndexing:  envBool("PODKOP_SERVER_SITE_INDEXING", false),
		SiteName:      env("PODKOP_SERVER_SITE_NAME", "Backfire"),
		SiteTagline:   env("PODKOP_SERVER_SITE_TAGLINE", "an open-city street racer on s&box"),
		TrustedProxy:  envBool("PODKOP_SERVER_TRUSTED_PROXY", false),
		XUIBaseURL:    strings.TrimRight(env("XUI_BASE_URL", ""), "/"),
		XUIUsername:   env("XUI_USERNAME", ""),
		XUIPassword:   env("XUI_PASSWORD", ""),
		XUIPublicHost: env("XUI_PUBLIC_HOST", ""),
		XUIClientFlow: strings.TrimSpace(env("XUI_CLIENT_FLOW", "")),
	}

	// A typo here would be silent: the key gets issued and simply never
	// connects, so only known values are accepted.
	switch c.XUIClientFlow {
	case "", "xtls-rprx-vision", "xtls-rprx-vision-udp443":
	default:
		return nil, fmt.Errorf("invalid XUI_CLIENT_FLOW %q: expected empty, xtls-rprx-vision or xtls-rprx-vision-udp443", c.XUIClientFlow)
	}

	// An explicitly empty name would render a blank wordmark, which is worse
	// than a generic one.
	if strings.TrimSpace(c.SiteName) == "" {
		c.SiteName = "Backfire"
	}

	switch c.Root {
	case "site", "decoy", "login":
	default:
		return nil, fmt.Errorf("invalid PODKOP_SERVER_ROOT %q: expected site, decoy or login", c.Root)
	}

	var err error
	if c.SessionTTL, err = envDuration("PODKOP_SERVER_SESSION_TTL", 12*time.Hour); err != nil {
		return nil, err
	}
	if c.LoginLockout, err = envDuration("PODKOP_SERVER_LOGIN_LOCKOUT", 15*time.Minute); err != nil {
		return nil, err
	}
	if c.LoginMaxFails, err = envInt("PODKOP_SERVER_LOGIN_MAX_FAILS", 5); err != nil {
		return nil, err
	}

	// Normalize admin path to /.../ form.
	if !strings.HasPrefix(c.AdminPath, "/") {
		c.AdminPath = "/" + c.AdminPath
	}
	if !strings.HasSuffix(c.AdminPath, "/") {
		c.AdminPath += "/"
	}
	if c.AdminPath == "/" {
		return nil, fmt.Errorf("PODKOP_SERVER_ADMIN_PATH must not be the site root; set a secret path")
	}

	if c.AdminPassword == "" {
		return nil, fmt.Errorf("PODKOP_SERVER_ADMIN_PASSWORD is required")
	}

	if v := env("XUI_INBOUND_ID", ""); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &c.XUIInbound); err != nil {
			return nil, fmt.Errorf("invalid XUI_INBOUND_ID: %w", err)
		}
	}

	return c, nil
}

// XUIEnabled reports whether 3x-UI integration is configured.
func (c *Config) XUIEnabled() bool {
	return c.XUIBaseURL != "" && c.XUIUsername != "" && c.XUIPassword != ""
}
