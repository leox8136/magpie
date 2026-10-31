package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/access"
)

// All failed remote credentials use one bucket, including a forged managed
// key. Locally the same key must still fail closed, rather than gain trust.
func TestManagedKeyFailureThrottle(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	newCaller(t, "remote")
	h := New().Front()
	now := time.Unix(1700000000, 0)
	authThrottle.Lock()
	authThrottle.tokens = 1
	authThrottle.last = now
	authThrottle.now = func() time.Time { return now }
	authThrottle.Unlock()
	t.Cleanup(resetAuthThrottle)
	for _, tc := range []struct {
		from, key string
		want      int
	}{
		{"192.0.2.1:1234", access.Prefix + "forged", 401},
		{"192.0.2.1:1234", access.Prefix + "forged", 429},
		{"192.0.2.1:1234", "ordinary-wrong-key", 429},
		{"127.0.0.1:1234", access.Prefix + "forged", 401},
	} {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		r.RemoteAddr = tc.from
		r.Header.Set("Authorization", "Bearer "+tc.key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: status=%d want %d", tc.from, tc.key, w.Code, tc.want)
		}
		if w.Code == 429 && w.Header().Get("Retry-After") == "" {
			t.Error("throttled response lacks Retry-After")
		}
	}
}

func TestSocketHistoryOwnership(t *testing.T) {
	old := prevResponses
	prevResponses = newPrevStore(16<<10, prevTTL)
	t.Cleanup(func() { prevResponses = old })
	rs := &responsesSocket{}
	defer rs.forget()
	frame := []byte(`{"input":[{"role":"user","content":"hello"}],"padding":"` + strings.Repeat("x", 1<<20) + `"}`)
	var q map[string]json.RawMessage
	if err := decodeJSONView(frame, &q); err != nil {
		t.Fatal(err)
	}
	_, input := prevInput(q["input"])
	rs.keep("one", input, len(input), nil)
	// Overwriting the receive buffer must not alter a completed conversation.
	for i := range frame {
		frame[i] = 'x'
	}
	saved, ok := rs.held("one")
	if !ok || string(saved[0]) != `{"role":"user","content":"hello"}` {
		t.Fatalf("history still belongs to the receive buffer: %s", saved)
	}
	rs.keep("two", append(saved[:len(saved):len(saved)], json.RawMessage(`{"role":"user","content":"next"}`)), 1, nil)
	rs.keep("two", saved, 0, nil) // reused provider id replaces the previous entry
	rs.keep("three", saved, 0, nil)
	if _, ok := rs.held("one"); ok {
		t.Fatal("more than two replies kept")
	}
	if _, ok := rs.held("two"); !ok {
		t.Fatal("reused id was evicted by an obsolete handle")
	}
	rs.forget()
	if prevResponses.size != 0 {
		t.Fatalf("closed socket still owns %d bytes", prevResponses.size)
	}
}

// History has one global bound shared with HTTP, while handles are private
// to a socket. Eviction and oversize retention fail as unknown history so a
// client can resend the conversation; they do not invalidate its warmup.
func TestSocketHistoryGlobalBound(t *testing.T) {
	fresh(t)
	old := prevResponses
	prevResponses = newPrevStore(16<<10, prevTTL)
	t.Cleanup(func() { prevResponses = old })
	server, ts := sockServer(t)
	clients := make([]*sockClient, 3)
	ids := make([]string, 3)
	warm := func(c *sockClient, input, previous string) (string, string) {
		t.Helper()
		prior := ""
		if previous != "" {
			prior = fmt.Sprintf(`,"previous_response_id":%q`, previous)
		}
		c.send(fmt.Sprintf(`{"type":"response.create","generate":false,"input":%q%s}`, input, prior))
		frames, err := c.until("response.completed", "error")
		if err != nil {
			t.Fatal(err)
		}
		end := frames[len(frames)-1]
		c.send(`{"type":"test.synchronize"}`)
		if _, err := c.until("error"); err != nil {
			t.Fatal(err)
		}
		return end.Get("response.id").String(), end.Get("error.code").String()
	}
	for i := range clients {
		c, _, err := dialSock(t, ts, "/v1/responses", nil)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = c
		ids[i], _ = warm(c, strings.Repeat("x", 6<<10), "")
		if ids[i] == "" {
			t.Fatal("warmup failed")
		}
	}
	if _, code := warm(clients[0], "next", ids[0]); code != "previous_response_not_found" {
		t.Fatalf("global eviction: %s", code)
	}
	if _, code := warm(clients[1], "next", ids[2]); code != "previous_response_not_found" {
		t.Fatalf("another socket read history: %s", code)
	}
	nextID, code := warm(clients[2], "next", ids[2])
	if nextID == "" || code != "" {
		t.Fatalf("retained history failed: id=%s code=%s", nextID, code)
	}
	// The new input fits; its inherited conversation exceeds the entry cap.
	id, _ := warm(clients[2], strings.Repeat("x", 3<<10), nextID)
	if id == "" {
		t.Fatal("oversize retention must not fail the request")
	}
	if _, code := warm(clients[2], "next", id); code != "previous_response_not_found" {
		t.Fatalf("oversize history retained: %s", code)
	}
	for _, c := range clients {
		c.c.CloseNow()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.sockets.drain(ctx)
	prevResponses.mu.Lock()
	size := prevResponses.size
	prevResponses.mu.Unlock()
	if size != 0 {
		t.Fatalf("closed sockets retained %d bytes", size)
	}
}

func TestSocketPartialFrameReleasesAdmission(t *testing.T) {
	fresh(t)
	old := bodyRoom
	room := &bodyRoomT{room: 2 << 20, sized: true}
	bodyRoom = room
	t.Cleanup(func() { bodyRoom = old })
	s, ts := sockServer(t)
	s.requestLimits.readTimeout = 500 * time.Millisecond
	c, _, err := dialSock(t, ts, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := c.c.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"input":"` + strings.Repeat("x", 1<<20))); err != nil {
		t.Fatal(err)
	}
	// Observe admission before waiting for release: an upload that never
	// reached the reader cannot prove the timeout releases anything.
	deadline := time.Now().Add(5 * time.Second)
	for {
		room.mu.Lock()
		held := room.held
		room.mu.Unlock()
		if held >= 1<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial frame did not acquire admission")
		}
		time.Sleep(time.Millisecond)
	}
	for {
		room.mu.Lock()
		held := room.held
		room.mu.Unlock()
		if held == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stalled frame still holds %d bytes", held)
		}
		time.Sleep(time.Millisecond)
	}
	// A separate large HTTP request can now acquire the same capacity.
	up := &respUp{}
	sockUp(t, up)
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"fake/m1","store":false,"input":"`+strings.Repeat("x", 2<<20)+`"}`)).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:1234"
	reply := httptest.NewRecorder()
	s.Front().ServeHTTP(reply, r)
	if reply.Code != 200 || up.calls() != 1 {
		t.Fatalf("unrelated HTTP request: %d (upstream calls %d)", reply.Code, up.calls())
	}
	if _, _, err := c.c.Read(ctx); err == nil {
		t.Fatal("timed-out upload left its connection open")
	}
}

func TestSocketIdleAndProgressingUpload(t *testing.T) {
	fresh(t)
	s, ts := sockServer(t)
	s.requestLimits.readTimeout = 500 * time.Millisecond
	c, _, err := dialSock(t, ts, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Quiet between messages is allowed. Progress is every 100ms (fivefold
	// margin), and the whole upload lasts longer than one timeout.
	time.Sleep(2 * s.requestLimits.readTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := c.c.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"type":"response.create","generate":false,"input":"`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := w.Write([]byte(strings.Repeat("x", 64<<10))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := w.Write([]byte(`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	frames, err := c.until("response.completed", "error")
	if err != nil {
		t.Fatal(err)
	}
	if frames[len(frames)-1].Get("type").String() != "response.completed" {
		t.Fatal(frames)
	}
}
