package gateway

import (
	"context"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIssuedKeysShareParentConcurrency(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	keys, secrets := newCaller(t, "issuer", "other")
	callerPolicy(t, keys[0].ID, 1, 0, 1)
	if _, err := access.Update("issuer-key", access.Change{Key: keys[0].ID, Issues: true}); err != nil {
		t.Fatal(err)
	}
	child, err := access.IssueKey(access.Issue{Name: "child", TTL: time.Hour, Issuer: keys[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "held-turn") {
			close(entered)
			select {
			case <-finish:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "up", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Front())
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := func(secret, word string) (int, error) {
		r, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/magpie/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","messages":[{"role":"user","content":"`+word+`"}]}`))
		r.Header.Set("Authorization", "Bearer "+secret)
		res, err := gw.Client().Do(r)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode, nil
	}
	done := make(chan error, 1)
	go func() { _, err := call(secrets[0], "held-turn"); done <- err }()
	defer func() {
		close(finish)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if code, err := call(child.Key, "child"); err != nil || code != 429 {
		t.Fatalf("child bypassed issuer: %d %v", code, err)
	}
	if code, err := call(secrets[1], "other"); err != nil || code != 200 {
		t.Fatalf("unrelated caller blocked: %d %v", code, err)
	}
	r, _ := http.NewRequestWithContext(ctx, "GET", gw.URL+"/magpie"+accountingPath, nil)
	r.Header.Set("Authorization", "Bearer "+child.Key)
	res, err := gw.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("issued key accessed accounting: %d", res.StatusCode)
	}
}
