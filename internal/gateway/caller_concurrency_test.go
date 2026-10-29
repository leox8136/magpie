package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
)

// Write the persisted shape, so the regression also runs on an old binary
// that did not yet enforce these fields.
func callerPolicy(t *testing.T, key string, max, queue, wait int) {
	t.Helper()
	b, err := os.ReadFile(access.Path())
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["id"] == key {
			row["concurrency"] = map[string]any{"maxConcurrency": max, "queueLimit": queue, "queueWait": wait}
		}
	}
	b, err = json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(access.Path(), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCallerQueueCancellationTimeoutAndReservation(t *testing.T) {
	fresh(t)
	now := pinLimitClock(t)
	budget.Forget()
	entered, gate := make(chan struct{}), make(chan struct{})
	var entering, closing sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "slow") {
			entering.Do(func() { close(entered) })
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "up", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "queued", "other")
	callerPolicy(t, keys[0].ID, 1, 1, 1)
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 10000})
	s := New()
	gw := httptest.NewServer(s.Front())
	defer gw.Close()
	end := func() { closing.Do(func() { close(gate) }) }
	defer end()
	call := func(ctx context.Context, secret, word string) (int, error) {
		r, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"`+word+`"}]}`))
		r.Header.Set("Authorization", "Bearer "+secret)
		resp, err := gw.Client().Do(r)
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	first := make(chan error, 1)
	go func() { _, err := call(context.Background(), secrets[0], "slow"); first <- err }()
	defer func() {
		end()
		if err := <-first; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := call(ctx, secrets[0], "fast"); second <- err }()
	waiting := func(want int) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for s.callerLaneSnapshot()[keys[0].ID].Waiting != want {
			if time.Now().After(until) {
				t.Fatal("caller queue did not reach", want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waiting(1)
	if st := budget.Of(access.Key{ID: keys[0].ID, Limit: &access.Limit{Period: "day", Tokens: 10000}}, now); st == nil || st.InFlight != 1 {
		t.Fatalf("queued request held budget: %+v", st)
	}
	if status, err := call(context.Background(), secrets[0], "fast"); err != nil || status != 429 {
		t.Fatalf("full queue: %d %v", status, err)
	}
	if status, err := call(context.Background(), secrets[1], "fast"); err != nil || status != 200 {
		t.Fatalf("other key: %d %v", status, err)
	}
	cancel()
	if err := <-second; err == nil {
		t.Fatal("canceled client completed")
	}
	waiting(0)
	if status, err := call(context.Background(), secrets[0], "fast"); err != nil || status != 429 {
		t.Fatalf("queue timeout: %d %v", status, err)
	}
	end()
}

func TestCallerConcurrencyLeavesOtherKeysAvailable(t *testing.T) {
	fresh(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "slow") {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "up", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "A", "B")
	callerPolicy(t, keys[0].ID, 1, 0, 1)
	gw := httptest.NewServer(New().Front())
	defer gw.Close()
	var finish sync.Once
	end := func() { finish.Do(func() { close(release) }) }
	defer end()
	call := func(secret, word string) (int, error) {
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"`+word+`"}]}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := gw.Client().Do(req)
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode, nil
	}
	first := make(chan error, 1)
	go func() { _, err := call(secrets[0], "slow"); first <- err }()
	defer func() {
		end()
		if err := <-first; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first call did not reach upstream")
	}
	status, err := call(secrets[0], "fast")
	if err != nil || status != 429 {
		t.Errorf("same key's excess request: %d, %v; want 429", status, err)
	}
	status, err = call(secrets[1], "fast")
	if err != nil || status != 200 {
		t.Errorf("other key blocked: %d, %v", status, err)
	}
	// Finish the first request before returning; no background writer may
	// outlive this test's isolated HOME.
	end()
}

func TestCallerConcurrencyHoldsStreamUntilEnd(t *testing.T) {
	for _, cancelClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "cancel"}[cancelClient], func(t *testing.T) {
			fresh(t)
			gate := make(chan struct{})
			var finish sync.Once
			end := func() { finish.Do(func() { close(gate) }) }
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if strings.Contains(string(b), "slow") {
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
				}
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "plan", Key: "up", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
				t.Fatal(err)
			}
			keys, secrets := newCaller(t, "streamer")
			callerPolicy(t, keys[0].ID, 1, 0, 0)
			s := New()
			gw := httptest.NewServer(s.Front())
			defer gw.Close()
			defer end()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			call := func(ctx context.Context, word string) *http.Response {
				t.Helper()
				r, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"`+word+`"}]}`))
				r.Header.Set("Authorization", "Bearer "+secrets[0])
				res, err := gw.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			res := call(ctx, "slow")
			defer res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatal(res.StatusCode)
			}
			excess := call(context.Background(), "fast")
			io.Copy(io.Discard, excess.Body)
			excess.Body.Close()
			if excess.StatusCode != 429 {
				t.Fatal("stream released its permit at headers", excess.StatusCode)
			}
			if cancelClient {
				cancel()
			} else {
				end()
			}
			io.Copy(io.Discard, res.Body)
			until := time.Now().Add(5 * time.Second)
			for len(s.callerLaneSnapshot()) != 0 {
				if time.Now().After(until) {
					t.Fatal("stream retained its caller permit")
				}
				time.Sleep(time.Millisecond)
			}
			last := call(context.Background(), "fast")
			io.Copy(io.Discard, last.Body)
			last.Body.Close()
			if last.StatusCode != 200 {
				t.Fatal("released stream still blocked caller", last.StatusCode)
			}
		})
	}
}
