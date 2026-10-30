package provider

import (
	"net/http"
	"slices"
	"testing"
)

// #4145: the list is kept canonical and once each, never with a header
// magpie doesn't pass on; matching ignores case.
func TestReplyHeadersList(t *testing.T) {
	got := cleanReplyHeaders([]string{" x-oneapi-request-id ", "X-OneAPI-Request-ID", "set-cookie", "content-length", "access-control-allow-origin", "x-magpie-rest", "bad name", "", "x-trace"})
	if want := []string{"X-Oneapi-Request-Id", "X-Trace"}; !slices.Equal(got, want) {
		t.Errorf("cleaned %q, want %q", got, want)
	}
	for _, bad := range [][]string{{"Set-Cookie"}, {"Content-Encoding"}, {"Transfer-Encoding"}, {"Authorization"}, {"Proxy-Authenticate"}, {"X-Magpie-Note"}, {"a b"}, {"a:b"}} {
		if ReplyHeadersOK(bad) == "" {
			t.Errorf("%q passed as OK", bad)
		}
	}
	if msg := ReplyHeadersOK([]string{"x-oneapi-request-id", " ", "X-Trace"}); msg != "" {
		t.Errorf("OK list refused: %s", msg)
	}
	p := Provider{ReplyHeaders: []string{"x-trace"}}
	h := http.Header{"X-Request-Id": {"r1"}, "x-trace": {"t1"}, "Set-Cookie": {"s"}, "X-Other": {"o"}}
	out := p.PassedReplyHeaders(h)
	if len(out) != 2 || out.Get("X-Request-Id") != "r1" || out.Get("X-Trace") != "t1" {
		t.Errorf("passed %v", out)
	}
	// a list saved by hand with a denied name still passes nothing of it
	p.ReplyHeaders = []string{"Set-Cookie"}
	if out := p.PassedReplyHeaders(h); out.Get("Set-Cookie") != "" {
		t.Errorf("passed %v", out)
	}
	if (Provider{}).PassedReplyHeaders(http.Header{"X-Other": {"o"}}) != nil {
		t.Error("passed headers nobody listed")
	}
}

// Connection can nominate additional hop-by-hop fields; listing one in the
// provider must not turn a connection-local field into an end-to-end header.
func TestReplyHeadersConnectionFieldsStayLocal(t *testing.T) {
	p := Provider{ReplyHeaders: []string{"X-Trace"}}
	h := http.Header{"connection": {"x-trace, X-Request-ID"}, "X-Trace": {"local"}, "X-Request-Id": {"local-id"}, "X-Oneapi-Request-Id": {"vendor-id"}}
	out := p.PassedReplyHeaders(h)
	if out.Get("X-Trace") != "" || out.Get("X-Request-Id") != "" || out.Get("X-Oneapi-Request-Id") != "vendor-id" {
		t.Fatalf("connection-local fields escaped: %v", out)
	}
}
