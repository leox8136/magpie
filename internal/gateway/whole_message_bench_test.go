package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Real HTTP response benchmark; sampled heap includes the gateway and peers.
func BenchmarkWholeMessageHTTP(b *testing.B) {
	for _, mib := range []int{1, 16} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			b.Setenv("XDG_CONFIG_HOME", b.TempDir())
			b.Setenv("XDG_CACHE_HOME", b.TempDir())
			reply := append([]byte(`{"id":"msg","type":"message","content":[{"type":"thinking","thinking":"small"},{"type":"text","text":"`), bytes.Repeat([]byte("x"), mib<<20)...)
			reply = append(reply, []byte(`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)...)
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					b.Error(err)
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Write(reply)
			}))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "bench", Name: "Bench", Key: "up", Anthropic: up.URL + "/v1", Models: []string{"m"}}); err != nil {
				b.Fatal(err)
			}
			secret, err := access.Update("add-key", access.Change{Name: "tenant"})
			if err != nil {
				b.Fatal(err)
			}
			keys, err := access.List()
			if err != nil {
				b.Fatal(err)
			}
			id := keys[len(keys)-1].ID
			if _, err := access.Update("limit-key", access.Change{Key: id, Limit: &access.Limit{Period: "day", Tokens: 1e12, Cost: 1e9}}); err != nil {
				b.Fatal(err)
			}
			if _, err := access.Update("concurrency-key", access.Change{Key: id, Concurrency: &access.Concurrency{MaxConcurrency: 2}}); err != nil {
				b.Fatal(err)
			}
			gw := httptest.NewServer(New().Front())
			defer gw.Close()
			client := gw.Client()
			client.Timeout = 30 * time.Second
			call := func(body []byte) {
				r, err := http.NewRequest("POST", gw.URL+"/v1/messages", bytes.NewReader(body))
				if err != nil {
					b.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+secret)
				resp, err := client.Do(r)
				if err != nil {
					b.Fatal(err)
				}
				n, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if readErr != nil || resp.StatusCode != 200 || n != int64(len(reply)+len(`,"signature":""`)) {
					b.Fatalf("HTTP %d: %v", resp.StatusCode, readErr)
				}
			}
			body := []byte(`{"model":"bench/m","max_tokens":100,"stream":false,"messages":[{"role":"user","content":"hi"}]}`)
			call(body)
			runtime.GC()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			base := mem.HeapInuse
			var peak atomic.Uint64
			peak.Store(base)
			sample := func() {
				runtime.ReadMemStats(&mem)
				peak.Store(max(peak.Load(), mem.HeapInuse))
			}
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				tick := time.NewTicker(2 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-tick.C:
						sample()
					case <-stop:
						sample()
						return
					}
				}
			}()
			defer func() { close(stop); <-done }()
			beforeCalls := calls.Load()
			b.SetBytes(int64(len(reply)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				call(body)
			}
			b.StopTimer()
			if calls.Load()-beforeCalls != int64(b.N) {
				b.Fatal("not every request reached the supplier")
			}
			b.ReportMetric(float64(peak.Load()-base)/(1<<20), "heap-peak-delta-MiB")
			b.ReportMetric(float64(base)/(1<<20), "heap-base-MiB")
			b.ReportMetric(float64(peak.Load())/(1<<20), "heap-peak-MiB")
			b.ReportMetric(float64(len(reply))/(1<<20), "response-MiB")
		})
	}
}
