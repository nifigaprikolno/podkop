// Package xui is a small best-effort client for the 3x-UI (MHSanaei/3x-ui) panel
// HTTP API. podkop-server uses it to issue VLESS keys on an existing inbound and
// to build the corresponding vless:// link that a router then receives in its
// route profile.
//
// The 3x-UI API is version-specific, so link generation is best-effort: when it
// cannot fully reconstruct a link, the operator can always paste one manually in
// the admin panel. Nothing here is required for podkop-server to function.
package xui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to a 3x-UI panel.
type Client struct {
	base       string
	user, pass string
	publicHost string
	http       *http.Client
}

// New builds a client. publicHost is used as the server address in generated
// links (3x-UI inbounds often listen on 0.0.0.0, which is not a usable address).
func New(baseURL, user, pass, publicHost string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if publicHost == "" {
		if u, err := url.Parse(baseURL); err == nil {
			publicHost = u.Hostname()
		}
	}
	return &Client{
		base:       strings.TrimRight(baseURL, "/"),
		user:       user,
		pass:       pass,
		publicHost: publicHost,
		http: &http.Client{
			Jar:     jar,
			Timeout: 20 * time.Second,
		},
	}, nil
}

type apiResp struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// Login authenticates and stores the session cookie in the jar.
func (c *Client) Login(ctx context.Context) error {
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("login: decode response: %w", err)
	}
	if !r.Success {
		return fmt.Errorf("login failed: %s", r.Msg)
	}
	return nil
}

// ClientSettings is the per-client object embedded in an inbound's settings.
type ClientSettings struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Flow       string `json:"flow,omitempty"`
	Enable     bool   `json:"enable"`
	TotalGB    int    `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	SubID      string `json:"subId,omitempty"`
}

// NewClientSettings builds an enabled, unlimited client with the given uuid/email.
func NewClientSettings(uuid, email string) ClientSettings {
	return ClientSettings{ID: uuid, Email: email, Enable: true}
}

// AddClient creates a new client on the given inbound.
func (c *Client) AddClient(ctx context.Context, inboundID int, cl ClientSettings) error {
	settings, err := json.Marshal(map[string]any{"clients": []ClientSettings{cl}})
	if err != nil {
		return err
	}
	form := url.Values{
		"id":       {strconv.Itoa(inboundID)},
		"settings": {string(settings)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/panel/api/inbounds/addClient", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("addClient: decode response: %w", err)
	}
	if !r.Success {
		return fmt.Errorf("addClient failed: %s", r.Msg)
	}
	return nil
}

// Inbound is the subset of a 3x-UI inbound we need to build a link.
type Inbound struct {
	ID             int    `json:"id"`
	Port           int    `json:"port"`
	Protocol       string `json:"protocol"`
	Remark         string `json:"remark"`
	StreamSettings string `json:"streamSettings"`
}

// GetInbound fetches an inbound by id.
func (c *Client) GetInbound(ctx context.Context, inboundID int) (*Inbound, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("getInbound: decode response: %w", err)
	}
	if !r.Success {
		return nil, fmt.Errorf("getInbound failed: %s", r.Msg)
	}
	var in Inbound
	if err := json.Unmarshal(r.Obj, &in); err != nil {
		return nil, fmt.Errorf("getInbound: parse inbound: %w", err)
	}
	return &in, nil
}

// streamSettings is the subset of an inbound's stream settings we parse.
type streamSettings struct {
	Network  string `json:"network"`
	Security string `json:"security"`
	TLS      struct {
		ServerName string `json:"serverName"`
	} `json:"tlsSettings"`
	Reality struct {
		ServerNames []string `json:"serverNames"`
		Settings    struct {
			PublicKey   string   `json:"publicKey"`
			Fingerprint string   `json:"fingerprint"`
			ShortIds    []string `json:"shortIds"`
			SpiderX     string   `json:"spiderX"`
		} `json:"settings"`
		ShortIds []string `json:"shortIds"`
	} `json:"realitySettings"`
	WS struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"wsSettings"`
	GRPC struct {
		ServiceName string `json:"serviceName"`
	} `json:"grpcSettings"`
}

// BuildVlessLink assembles a vless:// link for the given client on an inbound.
// It is best-effort and supports the common tcp/ws/grpc + tls/reality/none cases.
func (c *Client) BuildVlessLink(in *Inbound, cl ClientSettings) (string, error) {
	if !strings.EqualFold(in.Protocol, "vless") {
		return "", fmt.Errorf("unsupported inbound protocol %q (only vless link building is implemented)", in.Protocol)
	}
	var ss streamSettings
	if in.StreamSettings != "" {
		if err := json.Unmarshal([]byte(in.StreamSettings), &ss); err != nil {
			return "", fmt.Errorf("parse streamSettings: %w", err)
		}
	}

	host := c.publicHost
	if host == "" {
		return "", fmt.Errorf("no public host configured for link generation")
	}

	q := url.Values{}
	network := ss.Network
	if network == "" {
		network = "tcp"
	}
	q.Set("type", network)
	if cl.Flow != "" {
		q.Set("flow", cl.Flow)
	}

	switch strings.ToLower(ss.Security) {
	case "reality":
		q.Set("security", "reality")
		if len(ss.Reality.ServerNames) > 0 {
			q.Set("sni", ss.Reality.ServerNames[0])
		}
		if ss.Reality.Settings.PublicKey != "" {
			q.Set("pbk", ss.Reality.Settings.PublicKey)
		}
		if fp := ss.Reality.Settings.Fingerprint; fp != "" {
			q.Set("fp", fp)
		} else {
			q.Set("fp", "chrome")
		}
		if sid := firstNonEmpty(ss.Reality.Settings.ShortIds, ss.Reality.ShortIds); sid != "" {
			q.Set("sid", sid)
		}
		if ss.Reality.Settings.SpiderX != "" {
			q.Set("spx", ss.Reality.Settings.SpiderX)
		}
	case "tls":
		q.Set("security", "tls")
		if ss.TLS.ServerName != "" {
			q.Set("sni", ss.TLS.ServerName)
		}
	default:
		q.Set("security", "none")
	}

	switch network {
	case "ws":
		if ss.WS.Path != "" {
			q.Set("path", ss.WS.Path)
		}
		if h := ss.WS.Headers["Host"]; h != "" {
			q.Set("host", h)
		}
	case "grpc":
		if ss.GRPC.ServiceName != "" {
			q.Set("serviceName", ss.GRPC.ServiceName)
		}
	}

	frag := cl.Email
	if frag == "" {
		frag = in.Remark
	}
	link := fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		cl.ID, host, in.Port, q.Encode(), url.PathEscape(frag))
	return link, nil
}

func firstNonEmpty(lists ...[]string) string {
	for _, l := range lists {
		for _, s := range l {
			if s != "" {
				return s
			}
		}
	}
	return ""
}
