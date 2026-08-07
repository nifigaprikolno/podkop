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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
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
	// csrf is the token 3x-UI 3.6 wants on every POST. Empty against older
	// panels, which have no such check and ignore the header.
	csrf string
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

// csrfMeta finds the token 3x-UI 3.6 embeds in every page it serves.
var csrfMeta = regexp.MustCompile(`name="csrf-token"\s+content="([^"]+)"`)

// fetchCSRF loads the panel's entry page to pick up the CSRF token and the
// session cookie that goes with it. 3x-UI 3.6 rejects a POST without the
// matching pair with an empty 403 body; older panels simply have no token, and
// an empty result here is not an error.
func (c *Client) fetchCSRF(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/", nil)
	if err != nil {
		return
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return
	}
	if m := csrfMeta.FindSubmatch(body); m != nil {
		c.csrf = string(m[1])
	}
}

// do sends a request with the CSRF token attached and decodes the panel's
// envelope. A non-JSON body is reported with its status code: 3x-UI answers a
// rejected POST with an empty 403, and decoding that alone yields a bare "EOF"
// that says nothing about what went wrong.
func (c *Client) do(req *http.Request, what string) (*apiResp, error) {
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", what, err)
	}
	var r apiResp
	if err := json.Unmarshal(body, &r); err != nil {
		if len(body) == 0 {
			return nil, fmt.Errorf("%s: HTTP %d with an empty body", what, resp.StatusCode)
		}
		return nil, fmt.Errorf("%s: HTTP %d, unexpected response: %w", what, resp.StatusCode, err)
	}
	return &r, nil
}

// Login authenticates and stores the session cookie in the jar.
func (c *Client) Login(ctx context.Context) error {
	c.fetchCSRF(ctx)
	form := url.Values{"username": {c.user}, "password": {c.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := c.do(req, "login")
	if err != nil {
		return err
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
//
// 3x-UI 3.6 made clients first-class objects and moved them out from under the
// inbound: POST /panel/api/inbounds/addClient is gone and answers 404. Try the
// current endpoint first and fall back to the old one, so the same binary keeps
// working against panels that predate the move.
func (c *Client) AddClient(ctx context.Context, inboundID int, cl ClientSettings) error {
	err := c.addClientV36(ctx, inboundID, cl)
	if err == nil || !isNotFound(err) {
		return err
	}
	return c.addClientLegacy(ctx, inboundID, cl)
}

// errNotFound marks the 404 that tells the two API generations apart.
type errNotFound struct{ err error }

func (e errNotFound) Error() string { return e.err.Error() }
func (e errNotFound) Unwrap() error { return e.err }

func isNotFound(err error) bool {
	var nf errNotFound
	return errors.As(err, &nf)
}

// addClientV36 uses the 3.6 endpoint, which takes JSON and attaches one client
// to a list of inbounds. The uuid and flow we send are kept as given — 3x-UI
// only generates its own when they are omitted.
func (c *Client) addClientV36(ctx context.Context, inboundID int, cl ClientSettings) error {
	payload := map[string]any{
		"client": map[string]any{
			"email":      cl.Email,
			"id":         cl.ID,
			"flow":       cl.Flow,
			"enable":     cl.Enable,
			"totalGB":    cl.TotalGB,
			"expiryTime": cl.ExpiryTime,
			"subId":      cl.SubID,
		},
		"inboundIds": []int{inboundID},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/panel/api/clients/add", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := c.do(req, "addClient")
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return errNotFound{err}
		}
		return err
	}
	if !r.Success {
		return fmt.Errorf("addClient failed: %s", r.Msg)
	}
	return nil
}

// addClientLegacy is the pre-3.6 form-encoded call, kept for older panels.
func (c *Client) addClientLegacy(ctx context.Context, inboundID int, cl ClientSettings) error {
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
	r, err := c.do(req, "addClient")
	if err != nil {
		return err
	}
	if !r.Success {
		return fmt.Errorf("addClient failed: %s", r.Msg)
	}
	return nil
}

// jsonBlob is a nested JSON document that 3x-UI encodes one of two ways: as a
// string holding the encoded document (panels up to 3.5) or as the document
// itself (3.6 and later). Either arrives here as the raw JSON text, so the rest
// of the package can keep unmarshalling it in one step.
type jsonBlob string

func (b *jsonBlob) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*b = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*b = jsonBlob(s)
		return nil
	}
	*b = jsonBlob(data)
	return nil
}

// Inbound is the subset of a 3x-UI inbound we need to build a link.
type Inbound struct {
	ID             int      `json:"id"`
	Port           int      `json:"port"`
	Protocol       string   `json:"protocol"`
	Remark         string   `json:"remark"`
	StreamSettings jsonBlob `json:"streamSettings"`
	// Settings carries the protocol settings blob, which since VLESS Encryption
	// is where the value clients must echo back lives.
	Settings jsonBlob `json:"settings"`
}

// inboundSettings is the subset of an inbound's protocol settings we parse.
type inboundSettings struct {
	// Encryption is what a client has to send. Empty or "none" on a plain
	// VLESS inbound, a long mlkem768x25519plus… string once the operator turns
	// VLESS Encryption on — and a client that keeps sending "none" against
	// such an inbound is rejected at the handshake.
	Encryption string `json:"encryption"`
}

// clientEncryption reports the value a client link must carry.
func (in *Inbound) clientEncryption() string {
	var s inboundSettings
	if in.Settings != "" {
		if err := json.Unmarshal([]byte(in.Settings), &s); err != nil {
			return "none"
		}
	}
	if e := strings.TrimSpace(s.Encryption); e != "" {
		return e
	}
	return "none"
}

// GetInbound fetches an inbound by id.
func (c *Client) GetInbound(ctx context.Context, inboundID int) (*Inbound, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), nil)
	if err != nil {
		return nil, err
	}
	r, err := c.do(req, "getInbound")
	if err != nil {
		return nil, err
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
		// multiMode and authority must match on both ends: a client that omits
		// mode=multi against a multiMode inbound simply fails to connect.
		MultiMode bool   `json:"multiMode"`
		Authority string `json:"authority"`
	} `json:"grpcSettings"`
}

// normalizeNetwork folds the transport onto the name share links use. Xray
// renamed the plain TCP transport to "raw", and 3x-UI now writes that into new
// inbounds — but it is the same transport, it still carries XTLS Vision, and
// older clients only recognise "tcp" in a link.
func normalizeNetwork(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "tcp", "raw":
		return "tcp"
	default:
		return strings.ToLower(network)
	}
}

// Network reports the inbound's transport ("tcp" when unset or named "raw")
// and its security layer, so callers can decide what a client on it may use.
func (in *Inbound) Network() (network, security string, err error) {
	var ss streamSettings
	if in.StreamSettings != "" {
		if err := json.Unmarshal([]byte(in.StreamSettings), &ss); err != nil {
			return "", "", fmt.Errorf("parse streamSettings: %w", err)
		}
	}
	return normalizeNetwork(ss.Network), strings.ToLower(ss.Security), nil
}

// SupportsFlow reports whether a client on this inbound may carry an XTLS flow.
// Vision is a TCP-only feature: setting flow on a gRPC or WebSocket inbound
// produces a link that no client can use.
func (in *Inbound) SupportsFlow() bool {
	network, security, err := in.Network()
	if err != nil {
		return false
	}
	return network == "tcp" && (security == "reality" || security == "tls")
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
	network := normalizeNetwork(ss.Network)
	q.Set("type", network)
	// Stated explicitly rather than left to the client's default: with VLESS
	// Encryption in play the value is no longer always "none".
	q.Set("encryption", in.clientEncryption())
	// Vision only exists on TCP; carrying flow on any other transport yields a
	// link the client cannot connect with.
	if cl.Flow != "" && network == "tcp" {
		q.Set("flow", cl.Flow)
	}

	switch strings.ToLower(ss.Security) {
	case "reality":
		q.Set("security", "reality")
		if len(ss.Reality.ServerNames) > 0 {
			q.Set("sni", ss.Reality.ServerNames[0])
		}
		// Without the key pair there is nothing for the client to authenticate
		// against: the link would look complete and fail at the handshake, so
		// refuse to issue it and say what is missing.
		if ss.Reality.Settings.PublicKey == "" {
			return "", fmt.Errorf("inbound %d has no Reality public key — generate the key pair in 3x-UI", in.ID)
		}
		q.Set("pbk", ss.Reality.Settings.PublicKey)
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
		if ss.GRPC.MultiMode {
			q.Set("mode", "multi")
		}
		if ss.GRPC.Authority != "" {
			q.Set("authority", ss.GRPC.Authority)
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
