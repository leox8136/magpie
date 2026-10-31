package gateway

import (
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
