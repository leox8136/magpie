package cli

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/settings"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// magpie web on loopback behind a reverse proxy (#1479) prints the proxy's
// link with the key when told the proxy's address, so nobody joins
// "/?k=…" onto it by hand: MAGPIE_WEB_URL, else MAGPIE_PUBLIC_URL with no
// port (one with a port isn't a proxy). None is printed without either.
func TestWebProxyLink(t *testing.T) {
	key := "/?k=0123456789abcdef"
	for _, c := range []struct{ env, want string }{
		{"https://magpie.example.com", "https://magpie.example.com/?k=0123456789abcdef"},
		{"https://magpie.example.com/", "https://magpie.example.com/?k=0123456789abcdef"},
		{"magpie.example.com", "http://magpie.example.com/?k=0123456789abcdef"},
		{"https://example.com/magpie/", "https://example.com/magpie/?k=0123456789abcdef"},
		{"http://192.168.1.20:3425", ""},
		{"", ""},
	} {
		t.Setenv("MAGPIE_PUBLIC_URL", c.env)
		if got := proxyLink(key); got != c.want {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: %q, want %q", c.env, got, c.want)
		}
	}
	// the page's own proxy address, when the gateway's is elsewhere
	t.Setenv("MAGPIE_PUBLIC_URL", "https://api.example.com")
	for _, c := range []struct{ env, want string }{
		{"https://magpie.example.com/", "https://magpie.example.com/?k=0123456789abcdef"},
		{"magpie.example.com:8443", "https://magpie.example.com:8443/?k=0123456789abcdef"},
		{"https://magpie.example.com/?x=1", ""},
		{"ftp://magpie.example.com", ""},
	} {
		t.Setenv("MAGPIE_WEB_URL", c.env)
		if got := proxyLink(key); got != c.want {
			t.Errorf("MAGPIE_WEB_URL=%q: %q, want %q", c.env, got, c.want)
		}
	}
}

// `magpie web` opens the app's page when the app serves it on the port it
// would take, and starts its own on any other.
func TestWebSamePort(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"127.0.0.1:3430", "127.0.0.1:3430", true},
		{"0.0.0.0:3430", "127.0.0.1:3430", true},
		{"127.0.0.1:3430", "0.0.0.0:3431", false},
		{"", "127.0.0.1:3430", false},
		{"127.0.0.1:3430", "3430", false},
	} {
		if got := samePort(c.a, c.b); got != c.want {
			t.Errorf("samePort(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestWebLinksWithBasePath(t *testing.T) {
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://example.com:8443")
	t.Setenv("MAGPIE_WEB_URL", "")
	if got := webLink("127.0.0.1:3430", "key"); got != "http://127.0.0.1:3430/magpie/?k=key" {
		t.Fatal(got)
	}
	if got := proxyLink("/?k=key"); got != "https://example.com:8443/magpie/?k=key" {
		t.Fatal(got)
	}
}

func TestWebOpensPublishedAppMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAGPIE_BASE_PATH", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	b, _ := json.Marshal(map[string]any{"mode": "local", "port": port, "key": "0123456789abcdef", "basePath": "/magpie"})
	if err := os.MkdirAll(settings.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.Dir(), "web-page.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	// Capture the command's actual printed link without opening a browser.
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	old := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = old }()
	if err := webCmd([]string{"--no-open", "--addr", ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	out.Seek(0, 0)
	printed, _ := io.ReadAll(out)
	if !strings.Contains(string(printed), ln.Addr().String()+"/magpie/?k=0123456789abcdef") {
		t.Fatalf("wrong app link: %s", printed)
	}
}
