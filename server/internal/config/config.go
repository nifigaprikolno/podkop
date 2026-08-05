// Package config loads podkop-server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
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

	// 3x-UI integration (optional). When XUIBaseURL is empty, key issuance falls
	// back to manual proxy links pasted by the operator.
	XUIBaseURL    string
	XUIUsername   string
	XUIPassword   string
	XUIInbound    int    // inbound id on which new clients are created
	XUIPublicHost string // public host/IP to put into generated vless links
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
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
		XUIBaseURL:    strings.TrimRight(env("XUI_BASE_URL", ""), "/"),
		XUIUsername:   env("XUI_USERNAME", ""),
		XUIPassword:   env("XUI_PASSWORD", ""),
		XUIPublicHost: env("XUI_PUBLIC_HOST", ""),
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
