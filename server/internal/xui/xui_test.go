package xui

import (
	"net/url"
	"strings"
	"testing"
)

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New("http://3x-ui:2053", "admin", "pass", "vpn.example.com")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// parseLink splits a vless:// link into its uuid, host:port and query.
func parseLink(t *testing.T, link string) (string, string, url.Values) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	if u.Scheme != "vless" {
		t.Fatalf("scheme = %q, want vless", u.Scheme)
	}
	return u.User.String(), u.Host, u.Query()
}

func TestBuildVlessLinkTCPRealityWithFlow(t *testing.T) {
	c := newTestClient(t)
	in := &Inbound{
		ID: 1, Port: 443, Protocol: "vless", Remark: "reality",
		StreamSettings: `{
			"network": "tcp",
			"security": "reality",
			"realitySettings": {
				"serverNames": ["www.cloudflare.com"],
				"shortIds": ["e306"],
				"settings": {"publicKey": "PBK123", "fingerprint": "chrome", "spiderX": "/cPxy"}
			}
		}`,
	}
	cl := NewClientSettings("uuid-1", "router-a")
	cl.Flow = "xtls-rprx-vision"

	link, err := c.BuildVlessLink(in, cl)
	if err != nil {
		t.Fatalf("BuildVlessLink: %v", err)
	}
	uuid, host, q := parseLink(t, link)

	if uuid != "uuid-1" {
		t.Errorf("uuid = %q", uuid)
	}
	if host != "vpn.example.com:443" {
		t.Errorf("host = %q, want the configured public host", host)
	}
	for key, want := range map[string]string{
		"type":     "tcp",
		"security": "reality",
		"flow":     "xtls-rprx-vision",
		"sni":      "www.cloudflare.com",
		"pbk":      "PBK123",
		"fp":       "chrome",
		"sid":      "e306",
		"spx":      "/cPxy",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// A gRPC inbound needs serviceName, and mode/authority when the inbound sets
// them: a client that omits mode=multi against a multiMode inbound does not
// connect at all.
func TestBuildVlessLinkGRPCMultiMode(t *testing.T) {
	c := newTestClient(t)
	in := &Inbound{
		ID: 2, Port: 443, Protocol: "vless", Remark: "grpc-reality",
		StreamSettings: `{
			"network": "grpc",
			"security": "reality",
			"realitySettings": {
				"serverNames": ["www.cloudflare.com"],
				"settings": {"publicKey": "PBK123", "fingerprint": "firefox"}
			},
			"grpcSettings": {
				"serviceName": "/signaling/backend",
				"multiMode": true,
				"authority": "www.cloudflare.com"
			}
		}`,
	}

	link, err := c.BuildVlessLink(in, NewClientSettings("uuid-2", "router-b"))
	if err != nil {
		t.Fatalf("BuildVlessLink: %v", err)
	}
	_, _, q := parseLink(t, link)

	for key, want := range map[string]string{
		"type":        "grpc",
		"serviceName": "/signaling/backend",
		"mode":        "multi",
		"authority":   "www.cloudflare.com",
		"fp":          "firefox",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// Vision is TCP-only. A flow that leaked onto a gRPC inbound must be dropped
// from the link rather than handed to a client that cannot use it.
func TestBuildVlessLinkDropsFlowOffTCP(t *testing.T) {
	c := newTestClient(t)
	in := &Inbound{
		ID: 3, Port: 443, Protocol: "vless",
		StreamSettings: `{"network":"grpc","security":"reality","grpcSettings":{"serviceName":"/x"}}`,
	}
	cl := NewClientSettings("uuid-3", "router-c")
	cl.Flow = "xtls-rprx-vision"

	link, err := c.BuildVlessLink(in, cl)
	if err != nil {
		t.Fatalf("BuildVlessLink: %v", err)
	}
	if strings.Contains(link, "flow=") {
		t.Errorf("flow survived on a gRPC inbound: %s", link)
	}
}

func TestInboundNetworkAndSupportsFlow(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stream      string
		wantNetwork string
		wantFlow    bool
	}{
		{"tcp reality", `{"network":"tcp","security":"reality"}`, "tcp", true},
		{"tcp tls", `{"network":"tcp","security":"tls"}`, "tcp", true},
		{"tcp plain", `{"network":"tcp","security":"none"}`, "tcp", false},
		{"grpc reality", `{"network":"grpc","security":"reality"}`, "grpc", false},
		{"ws tls", `{"network":"ws","security":"tls"}`, "ws", false},
		{"empty defaults to tcp", `{"security":"reality"}`, "tcp", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &Inbound{StreamSettings: tc.stream}
			network, _, err := in.Network()
			if err != nil {
				t.Fatalf("Network: %v", err)
			}
			if network != tc.wantNetwork {
				t.Errorf("network = %q, want %q", network, tc.wantNetwork)
			}
			if got := in.SupportsFlow(); got != tc.wantFlow {
				t.Errorf("SupportsFlow() = %v, want %v", got, tc.wantFlow)
			}
		})
	}
}

func TestBuildVlessLinkRejectsNonVless(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.BuildVlessLink(&Inbound{Protocol: "vmess"}, NewClientSettings("id", "e")); err == nil {
		t.Error("expected an error for a non-vless inbound")
	}
}
