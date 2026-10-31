package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

func TestPreviousHistoryAdmission(t *testing.T) {
	fresh(t)
	up := newPrevUp(t, func(n int, _ []byte) (int, string, string) { return 200, "text/event-stream", chatSSEReply("ok") })
	s := prevSandbox(t, provider.Provider{ID: "fake", Key: "k", Models: []string{"m1"}, Chat: up.srv.URL + "/v1"})
	old, wait := bodyRoom, bodyRoomWait
	bodyRoom = &bodyRoomT{room: 2 << 20, sized: true}
	bodyRoomWait = 20 * time.Millisecond
	t.Cleanup(func() { bodyRoom = old; bodyRoomWait = wait })
	busy, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := bodyRoom.take(busy, 2<<20); err != nil {
		t.Fatal(err)
	}
	item := json.RawMessage(`{"type":"message","role":"user","content":"` + strings.Repeat("x", 2<<20) + `"}`)
	prevResponses.put(&prevTurn{key: prevKey{"key:", "seed"}, items: []json.RawMessage{item}, own: len(item)})
	ts := httptest.NewServer(s.Front())
	defer ts.Close()
	res, err := http.Post(ts.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"fake/m1","previous_response_id":"seed","input":"next","store":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	if res.StatusCode != 503 || up.calls() != 0 {
		t.Fatalf("expanded 2MiB history bypassed full admission: status=%d upstream=%d", res.StatusCode, up.calls())
	}
}
func TestSocketWarmupRevocation(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0")
	keys, secrets := newCaller(t, "tenant")
	_, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/v1/responses", http.Header{"Authorization": {"Bearer " + secrets[0]}, "X-Test-Remote": {"192.0.2.1:4567"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = access.Update("remove-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	c.send(`{"type":"response.create","model":"fake/m1","generate":false,"input":"secret"}`)
	frames, err := c.until("error", "response.completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 || frames[len(frames)-1].Get("status").Int() != 401 {
		t.Fatalf("revoked key's warmup was not refused: %v", frames)
	}
}
func TestSocketQueueLimit(t *testing.T) {
	fresh(t)
	keys, secrets := newCaller(t, "tenant")
	if _, err := access.Update("concurrency-key", access.Change{Key: keys[0].ID, Concurrency: &access.Concurrency{MaxConcurrency: 1, QueueLimit: 0}}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	u := &respUp{reply: func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r%d\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n", n)
	}}
	sockUp(t, u)
	_, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/v1/responses", http.Header{"Authorization": {"Bearer " + secrets[0]}})
	if err != nil {
		t.Fatal(err)
	}
	frame := `{"type":"response.create","model":"fake/m1","input":"hi","store":false}`
	c.send(frame)
	<-started
	c.send(frame)
	c.send(`{"type":"response.interrupt","response_id":"not-active"}`)
	frames, err := c.until("response.interrupt.failed")
	if err != nil {
		t.Fatal(err)
	}
	rejected := false
	for _, f := range frames {
		if f.Get("type").String() == "error" && f.Get("status").Int() == 429 {
			rejected = true
		}
	}
	if !rejected {
		close(release)
		t.Fatal("QueueLimit=0 did not reject the queued turn")
	}
	close(release)
	if _, err := c.until("response.completed"); err != nil {
		t.Fatal(err)
	}
	if u.calls() != 1 {
		t.Fatalf("QueueLimit=0 nevertheless admitted %d turns", u.calls())
	}
}
func TestPrunedExpiryFailsClosed(t *testing.T) {
	fresh(t)
	now := time.Now()
	old := access.Now
	access.Now = func() time.Time { return now }
	defer func() { access.Now = old }()
	k, err := access.IssueKey(access.Issue{TTL: time.Minute, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Front()
	now = now.Add(2 * time.Hour)
	before := keysCall(h, "POST", KeysPath, fromHere, k.Key, `{"ttl":"1h"}`)
	if before.Code != 401 {
		t.Fatalf("fixture: expired key not refused: %d", before.Code)
	}
	if n, err := access.Prune(); err != nil || n != 1 {
		t.Fatalf("prune %d %v", n, err)
	}
	after := keysCall(h, "POST", KeysPath, fromHere, k.Key, `{"ttl":"1h"}`)
	if after.Code != 401 {
		t.Fatalf("pruned expired read-only key gained local admin issuance: status=%d", after.Code)
	}
}
func TestPreviousHistoryReservation(t *testing.T) {
	fresh(t)
	started, release := make(chan struct{}), make(chan struct{})
	up := newPrevUp(t, func(n int, _ []byte) (int, string, string) {
		if n == 1 {
			close(started)
			<-release
		}
		return 200, "text/event-stream", chatSSEReply("ok")
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Key: "k", Models: []string{"m1"}, Chat: up.srv.URL + "/v1"})
	keys, secrets := newCaller(t, "tenant")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 1000})
	item := json.RawMessage(`{"type":"message","role":"user","content":"` + strings.Repeat("x", 2<<20) + `"}`)
	prevResponses.put(&prevTurn{key: prevKey{"key:" + keys[0].ID, "seed"}, items: []json.RawMessage{item}, own: len(item)})
	ts := httptest.NewServer(s.Front())
	defer ts.Close()
	ask := func(body string) (int, error) {
		r, _ := http.NewRequest("POST", ts.URL+"/v1/responses", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		res, err := ts.Client().Do(r)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ask(`{"model":"fake/m1","previous_response_id":"seed","input":"next","store":false}`)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		close(release)
		<-done
		t.Fatal("first call not started")
	}
	status, err := ask(`{"model":"fake/m1","input":"another","store":false}`)
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if status != 429 {
		t.Fatalf("2MiB history in flight with token cap1000 did not reserve its input: second request status=%d upstream=%d", status, up.calls())
	}
}

// Admission happens while the frame is read, including a warmup which never
// reaches an upstream. A rejected receive must not retain its partial buffer.
func TestSocketFrameMemoryAdmission(t *testing.T) {
	fresh(t)
	old := bodyRoom
	room := &bodyRoomT{room: 2 << 20, sized: true}
	bodyRoom = room
	t.Cleanup(func() { bodyRoom = old })
	busy, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := room.take(busy, 2<<20); err != nil {
		t.Fatal(err)
	}
	server, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Refusal can close the receive before the client finishes uploading;
	// either the 503 frame or that connection failure is observable.
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = c.c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"fake/m1","generate":false,"input":"`+strings.Repeat("x", 2<<20)+`"}`))
	frames, readErr := c.until("error", "response.completed")
	if readErr == nil && (len(frames) == 0 || frames[len(frames)-1].Get("status").Int() != 503) {
		t.Fatalf("full room admitted a warmup: %v", frames)
	}
	c.c.CloseNow()
	server.sockets.drain(ctx)
	room.mu.Lock()
	held := room.held
	room.mu.Unlock()
	if held != 2<<20 {
		t.Fatalf("rejected socket retained memory: %d", held)
	}
}

func TestSocketWarmupReadOnly(t *testing.T) {
	fresh(t)
	keys, secrets := newCaller(t, "tenant")
	_, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/v1/responses", http.Header{"Authorization": {"Bearer " + secrets[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	c.send(`{"type":"response.create","model":"fake/m1","generate":false,"input":"secret"}`)
	frames, err := c.until("error", "response.completed")
	if err != nil {
		t.Fatal(err)
	}
	if frames[len(frames)-1].Get("status").Int() != 403 {
		t.Fatalf("read-only key's warmup was not refused: %v", frames)
	}
}

// A terminal event can precede upstream EOF. The one-frame handoff must not
// stop reading interrupts or the disconnect while that old stream closes.
func TestSocketCompletedStreamStillReadsControls(t *testing.T) {
	fresh(t)
	gone := make(chan struct{})
	u := &respUp{reply: func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"finished\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	}}
	sockUp(t, u)
	_, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.turn(create("", userHi))
	c.send(create("", userHi))
	c.send(`{"type":"response.interrupt","response_id":"another"}`)
	if _, err := c.until("response.interrupt.failed"); err != nil {
		t.Fatal(err)
	}
	c.c.CloseNow()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("completed upstream outlived the disconnected client")
	}
}
