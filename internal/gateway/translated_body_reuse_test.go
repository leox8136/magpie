package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Spending the conversion builder's output must not damage the decoded body
// reused after the budget pre-read or the conversation sent on failover.
func TestTranslatedBodyFailoverPreservesSharedRequest(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var sent [][]byte
	up := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Messages json.RawMessage `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			sent = append(sent, body.Messages)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if status != 200 {
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"temporary server error"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"reply\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		}))
	}
	first, second := up(500), up(200)
	defer first.Close()
	defer second.Close()
	for i, endpoint := range []string{first.URL, second.URL} {
		id := []string{"first", "second"}[i]
		if err := provider.Save(provider.Provider{ID: id, Key: "up", Chat: endpoint + "/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"first/m", "second/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "tenant")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 1e9, Cost: 1e6})
	callerPolicy(t, keys[0].ID, 1, 0, 0)
	body := largeConversation(provider.Responses, "group/g", (6<<20)/6)
	gw := httptest.NewServer(New().Front())
	defer gw.Close()
	gw.Client().Timeout = 30 * time.Second
	r, _ := http.NewRequest("POST", gw.URL+"/v1/responses", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	res, err := gw.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reply, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s (%v)", res.StatusCode, reply, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || len(sent[0]) <= 4<<20 || !bytes.Equal(sent[0], sent[1]) {
		t.Fatal("the complete converted conversation did not survive failover", len(sent))
	}
}
