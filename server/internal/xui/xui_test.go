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
		StreamSettings: `{"network":"grpc","security":"reality","grpcSettings":{"serviceName":"/x"},
			"realitySettings":{"settings":{"publicKey":"pk"}}}`,
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

// A real 3x-UI inbound with VLESS Encryption turned on: the client has to echo
// the settings.encryption value back, and a link that quietly says "none"
// is refused at the handshake.
func TestBuildVlessLinkCarriesVlessEncryption(t *testing.T) {
	c := &Client{publicHost: "144.31.12.79"}
	in := &Inbound{
		ID: 1, Port: 443, Protocol: "vless", Remark: "in-443-tcp",
		Settings: `{"clients":[],
			"decryption":"mlkem768x25519plus.native.600s.6E65mKkV2QkM5HO6A5iAojpcQsawkEYwoj-Cvwm2GW8",
			"encryption":"mlkem768x25519plus.native.0rtt.aC0--LIz2SIWHBdyhpU69Rzj5bIaoKUjn-Sm4s6oKFY"}`,
		StreamSettings: `{"network":"grpc","security":"reality",
			"grpcSettings":{"serviceName":"","authority":"","multiMode":true},
			"realitySettings":{"serverNames":["www.samsung.com","cdn.samsung.com"],
				"shortIds":["bd5f","3223b3"],
				"settings":{"publicKey":"xvacW2GRcQI3GmRBWn91GUG6_C6knlY3rCcwXy4hQXU",
					"fingerprint":"firefox","spiderX":"/nrorm2W9oSixIbQ"}}}`,
	}

	link, err := c.BuildVlessLink(in, ClientSettings{ID: "uuid-1", Email: "phone", Flow: "xtls-rprx-vision"})
	if err != nil {
		t.Fatalf("BuildVlessLink: %v", err)
	}

	for _, want := range []string{
		"encryption=mlkem768x25519plus.native.0rtt.aC0--LIz2SIWHBdyhpU69Rzj5bIaoKUjn-Sm4s6oKFY",
		"type=grpc", "mode=multi", "security=reality",
		"sni=www.samsung.com", "fp=firefox", "sid=bd5f",
	} {
		if !strings.Contains(link, want) {
			t.Errorf("link is missing %q:\n%s", want, link)
		}
	}
	// Vision is TCP-only; a gRPC link carrying flow does not connect.
	if strings.Contains(link, "flow=") {
		t.Errorf("flow leaked onto a gRPC link:\n%s", link)
	}
}

func TestBuildVlessLinkDefaultsEncryptionToNone(t *testing.T) {
	c := &Client{publicHost: "example.test"}
	in := &Inbound{
		ID: 1, Port: 443, Protocol: "vless",
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"serverNames":["a.example"],"shortIds":["ab"],"settings":{"publicKey":"pk"}}}`,
	}
	link, err := c.BuildVlessLink(in, ClientSettings{ID: "uuid-2", Flow: "xtls-rprx-vision"})
	if err != nil {
		t.Fatalf("BuildVlessLink: %v", err)
	}
	if !strings.Contains(link, "encryption=none") {
		t.Errorf("plain inbound should state encryption=none:\n%s", link)
	}
	if !strings.Contains(link, "flow=xtls-rprx-vision") {
		t.Errorf("TCP inbound should keep the flow:\n%s", link)
	}
}

// Xray renamed the TCP transport to "raw"; new 3x-UI inbounds carry that name.
// Treating it as an unknown transport would strip Vision off a link that can
// carry it, and emit a type= value older clients do not understand.
func TestBuildVlessLinkTreatsRawAsTCP(t *testing.T) {
	c := &Client{publicHost: "144.31.12.79"}
	in := &Inbound{
		ID: 1, Port: 443, Protocol: "vless", Remark: "in-443-raw",
		Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"raw","security":"reality",
			"realitySettings":{"serverNames":["www.samsung.com"],"shortIds":["bd5f"],
				"settings":{"publicKey":"pk","fingerprint":"chrome"}}}`,
	}

	if !in.SupportsFlow() {
		t.Error("a raw+reality inbound carries Vision and must report so")
	}
	if network, _, err := in.Network(); err != nil || network != "tcp" {
		t.Errorf("Network() = %q, %v; want tcp", network, err)
	}

	link, err := c.BuildVlessLink(in, ClientSettings{ID: "uuid-3", Flow: "xtls-rprx-vision"})
	if err != nil {
		t.Fatalf("BuildVlessLink: %v", err)
	}
	if !strings.Contains(link, "type=tcp") {
		t.Errorf("want type=tcp for wider client support:\n%s", link)
	}
	if !strings.Contains(link, "flow=xtls-rprx-vision") {
		t.Errorf("Vision was dropped off a transport that supports it:\n%s", link)
	}
}

// 3x-UI happily saves a Reality inbound with an empty key pair when the
// operator skips the generate button. A link built from it carries no pbk and
// cannot connect, so issuing must fail loudly instead.
func TestBuildVlessLinkRefusesRealityWithoutKeys(t *testing.T) {
	c := &Client{publicHost: "144.31.12.79"}
	in := &Inbound{
		ID: 1, Port: 443, Protocol: "vless",
		Settings: `{"clients":[],"decryption":"none","encryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"reality",
			"realitySettings":{"target":"www.samsung.com:443","serverNames":["www.samsung.com"],
				"privateKey":"","shortIds":["50ca3755"],
				"settings":{"publicKey":"","fingerprint":"firefox","spiderX":"/"}}}`,
	}
	_, err := c.BuildVlessLink(in, ClientSettings{ID: "uuid-4", Flow: "xtls-rprx-vision"})
	if err == nil {
		t.Fatal("a Reality inbound without a key pair must not yield a link")
	}
	if !strings.Contains(err.Error(), "Reality public key") {
		t.Errorf("error should name the missing key pair, got: %v", err)
	}
}
