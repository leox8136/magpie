package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
)

func TestBasePathConfiguration(t *testing.T) {
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	for _, tc := range []struct{ value, want string }{{"", ""}, {"/", ""}, {"/magpie/", "/magpie"}, {"/tools/magpie", "/tools/magpie"}} {
		t.Setenv("MAGPIE_BASE_PATH", tc.value)
		if p, err := BasePath(); err != nil || p != tc.want {
			t.Fatalf("%q: %q %v", tc.value, p, err)
		}
	}
	for _, value := range []string{"/backend-api", "/muse-code/models", "/api", "/api/team", "/v1", "/_magpie", "magpie", "//", "/a/../b", "/a//b", "/a%2fb", "/a?b", "/a;b", `/a\b`} {
		t.Setenv("MAGPIE_BASE_PATH", value)
		if _, err := BasePath(); err == nil {
			t.Fatalf("invalid prefix accepted: %q", value)
		}
	}
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://example.com:8443/")
	if got := PublicURL(); got != "https://example.com:8443/magpie" {
		t.Fatal(got)
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "https://example.com/other")
	if _, err := BasePath(); err == nil {
		t.Fatal("conflicting advertised mount accepted")
	}
}

func TestMountedPathPreservesRequest(t *testing.T) {
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	var calls int
	h := MountBasePath("/magpie", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.EscapedPath() != "/v1/models/a%2Fb" || r.URL.RawQuery != "x=a%2Fb" || r.Host != "public.example" || r.RemoteAddr != "192.0.2.1:4567" || r.Header.Get("Authorization") != "Bearer key" || string(b) != "body" || RequestBasePath(r) != "/magpie" {
			t.Fatalf("request changed: %+v body %q", r, b)
		}
		if got := imageMCPBase(r); got != "http://public.example/magpie" {
			t.Fatalf("image download base: %s", got)
		}
		w.WriteHeader(204)
	}), false)
	r := httptest.NewRequest("POST", "http://public.example/magpie/v1/models/a%2Fb?x=a%2Fb", strings.NewReader("body"))
	r.RemoteAddr = "192.0.2.1:4567"
	r.Header.Set("Authorization", "Bearer key")
	h.ServeHTTP(httptest.NewRecorder(), r)
	for _, p := range []string{"/v1/models", "/magpie-other/v1/models", "/magpie%2Fv1/models", "/magpie/../api/state", "/magpie//v1/models"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 400 && w.Code != 404 {
			t.Fatalf("ambiguous/outside path %q: %d", p, w.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("outside request reached mounted handler: %d", calls)
	}
}

func TestPrefixedGatewayKeepsKeyGuards(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	key, err := access.Update("add-key", access.Change{Name: "Remote"})
	if err != nil {
		t.Fatal(err)
	}
	h := New().Front()
	ask := func(path, peer, secret string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Host, r.RemoteAddr = "127.0.0.1:3425", peer
		if secret != "" {
			r.Header.Set("x-api-key", secret)
		}
		r.Header.Set("X-Forwarded-Prefix", "/other")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := ask("/magpie/v1/models", "192.0.2.1:4567", ""); got != 401 {
		t.Fatalf("remote without key: %d", got)
	}
	if got := ask("/magpie/v1/models", "192.0.2.1:4567", key); got != 200 {
		t.Fatalf("remote key: %d", got)
	}
	if got := ask("/v1/models", "127.0.0.1:4567", ""); got != 200 {
		t.Fatalf("local agent root route: %d", got)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
	if got := ask("/magpie/v1/models", "192.0.2.1:4567", key); got != 403 {
		t.Fatalf("unshared remote: %d", got)
	}
}

// A client must receive the first event before the handler completes;
// mounting must preserve Flusher and must not collect the response body.
func TestMountedStream(t *testing.T) {
	finish := make(chan struct{})
	h := MountBasePath("/magpie", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-finish
		io.WriteString(w, "data: last\n\n")
	}), false)
	srv := httptest.NewServer(h)
	defer srv.Close()
	defer close(finish)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/magpie/v1/responses", nil)
	res, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || line != "data: first\n" || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("first streamed event: %q, %v, %v", line, err, res.Header)
	}
}

func TestMountedPathStrippedOnce(t *testing.T) {
	// A mount may share a name with a route. A request already stripped
	// by the web listener must reach Front's router unchanged.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.URL.Path)
	})
	h := MountBasePath("/v1", MountBasePath("/v1", next, true), false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/v1/models", nil))
	if w.Body.String() != "/v1/models" {
		t.Fatalf("route stripped twice: %s", w.Body)
	}
}
