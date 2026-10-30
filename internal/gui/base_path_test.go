package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

func TestWebBasePathLoginAndAssets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://public.example:8443")
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	old, oldWeb := served.Load(), webPage.Load()
	t.Cleanup(func() { served.Store(old); webPage.Store(oldWeb) })
	const key = "0123456789abcdef"
	h := webServed("3430", key, webCookieAge, Handler(webHost{}, gateway.New()))
	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://public.example"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4567"
		r.Header.Set("Accept", "text/html")
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/magpie?k="+key, "", nil)
	if w.Code != 308 || w.Header().Get("Location") != "/magpie/?k="+key {
		t.Fatalf("slash redirect: %d %v", w.Code, w.Header())
	}
	w = request("GET", "/magpie/", "", nil)
	if w.Code != 401 || !strings.Contains(w.Body.String(), `action="/magpie/_magpie/sign-in"`) {
		t.Fatalf("sign-in form: %d %s", w.Code, w.Body)
	}
	w = request("GET", "/magpie/?k="+key+"&view=providers", "", nil)
	cookies := w.Result().Cookies()
	if w.Code != 303 || w.Header().Get("Location") != "/magpie/?view=providers" || len(cookies) != 1 || cookies[0].Path != "/magpie/" || !cookies[0].HttpOnly {
		t.Fatalf("sign in: %d %v", w.Code, w.Header())
	}
	cookie := cookies[0]
	w = request("GET", "/magpie/boot.js", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"basePath":"/magpie"`) {
		t.Fatalf("boot: %d %s", w.Code, w.Body)
	}
	w = request("GET", "/magpie/", "", cookie)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d", w.Code)
	}
	w = request("GET", "/magpie/paths.js", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "function magpiePath") {
		t.Fatalf("client helper: %d", w.Code)
	}
	w = request("GET", "/magpie/manifest.json", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"start_url": "./"`) {
		t.Fatalf("PWA: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/", "/boot.js", "/api/state", "/magpie-other/boot.js"} {
		if w := request("GET", path, "", cookie); w.Code != 404 {
			t.Fatalf("outside mount %s: %d", path, w.Code)
		}
	}
	w = request("POST", "/magpie/_magpie/sign-in", url.Values{"key": {key}, "back": {"//evil.example/"}}.Encode(), nil)
	if w.Code != 303 || w.Header().Get("Location") != "/magpie/" {
		t.Fatalf("unsafe redirect: %d %v", w.Code, w.Header())
	}
	w = request("GET", "/magpie/v1/models", "", cookie)
	if w.Code != 401 {
		t.Fatalf("web cookie granted model access: %d %s", w.Code, w.Body)
	}
	if links := NetworkLinks("3430", "/?k=x"); len(links) != 1 || links[0] != "https://public.example:8443/magpie/?k=x" {
		t.Fatal(links)
	}
}

func TestPluginPageUnderBasePath(t *testing.T) {
	pagePlugins(t)
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	pages, _ := windowParts()
	if len(pages) != 1 {
		t.Fatalf("pages: %d", len(pages))
	}
	p := pages[0]
	// JSON links are already usable by a client; the browser helper must
	// leave these mounted URLs unchanged.
	listed := webServed("3430", "0123456789abcdef", 0, pageMux())
	req := httptest.NewRequest("GET", "/magpie/api/plugin-pages", nil)
	req.AddCookie(&http.Cookie{Name: "magpie_web_3430", Value: "0123456789abcdef"})
	res := httptest.NewRecorder()
	listed.ServeHTTP(res, req)
	var links []pluginPage
	if err := json.Unmarshal(res.Body.Bytes(), &links); err != nil || res.Code != 200 || len(links) != 1 || links[0].URL != "/magpie"+pages[0].URL || links[0].Icon != "/magpie"+pages[0].Icon {
		t.Fatalf("mounted plugin links: %d %s (%v)", res.Code, res.Body, err)
	}
	h := webServed("3430", "0123456789abcdef", 0, pageMux())
	r := httptest.NewRequest("GET", "http://public.example/magpie"+p.URL, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "public.example/magpie"+pluginPagePrefix) {
		t.Fatalf("plugin mount: %d %v", w.Code, w.Header())
	}
	// Its relative bridge remains reachable without making the page's API
	// available to a sandboxed frame.
	bridge := p.URL[:strings.LastIndex(p.URL, "/")+1] + pluginBridgePath
	r = httptest.NewRequest("GET", "/magpie"+bridge, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("bridge: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/magpie/api/plugin-pages/read", strings.NewReader(`{}`))
	r.Header.Set("Origin", "null")
	r.AddCookie(&http.Cookie{Name: "magpie_web_3430", Value: "0123456789abcdef"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		b, _ := io.ReadAll(w.Result().Body)
		t.Fatalf("frame reached API: %d %s", w.Code, b)
	}
}

func TestWebBasePathForwardedGateway(t *testing.T) {
	onePortHome(t)
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	other := httptest.NewServer(gateway.MountBasePath("/magpie", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{
			"base": gateway.RequestBasePath(r), "path": r.URL.EscapedPath(),
			"query": r.URL.RawQuery, "key": r.Header.Get("Authorization"),
			"page": r.Header.Get(gateway.PagePortHeader),
		})
	}), false))
	defer other.Close()
	u, _ := url.Parse(other.URL)
	t.Setenv("MAGPIE_ADDR", u.Host)
	old := served.Swap(nil)
	t.Cleanup(func() { served.Store(old) })
	h := webServed("3430", "0123456789abcdef", 0, http.NotFoundHandler())
	w := onePortCall(t, h, "GET", "/magpie/v1/models/a%2Fb?x=a%2Fb", "127.0.0.1:4567", "public.example", "", "Authorization", "Bearer caller")
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got["base"] != "/magpie" || got["path"] != "/v1/models/a%2Fb" || got["query"] != "x=a%2Fb" || got["key"] != "Bearer caller" || got["page"] == "" {
		t.Fatalf("forwarded mount: %d %s (%v)", w.Code, w.Body, err)
	}
}

func TestAppPageBasePathLink(t *testing.T) {
	var reached bool
	p := appPageTest(t, &reached)
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	port := freePort(t)
	code, state := appPagePost(t, `{"mode":"local","port":`+strconv.Itoa(port)+`}`)
	if code != 200 || !state.Serving {
		t.Fatalf("app page: %d %+v", code, state)
	}
	// The CLI's environment need not match the app that owns the port.
	t.Setenv("MAGPIE_BASE_PATH", "")
	if got := AppPageBasePath(); got != "/magpie" {
		t.Fatalf("published app mount: %q", got)
	}
	u, err := url.Parse(state.Link)
	if err != nil || u.Path != "/magpie/" || u.Query().Get("k") == "" {
		t.Fatalf("app page link: %s (%v)", state.Link, err)
	}
	res := appPageGet(t, port, u.RequestURI(), "")
	defer res.Body.Close()
	if res.StatusCode != 303 || res.Header.Get("Location") != "/magpie/" {
		t.Fatalf("app page sign in: %d %v", res.StatusCode, res.Header)
	}
	t.Setenv("MAGPIE_BASE_PATH", "/bad/../mount")
	p.apply()
	if state := p.state(); state.Serving || state.Error == "" {
		t.Fatalf("invalid mount still serving: %+v", state)
	}
}
