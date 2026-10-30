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

// This benchmark includes real HTTP on both sides, a cost-budget pre-read,
// caller admission and Responses -> Chat conversion. The sampled heap peak
// includes both peers; it is neither RSS nor a production capacity estimate.
func BenchmarkTranslatedRequest(b *testing.B) {
	for _, mib := range []int{1, 45} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			b.Setenv("XDG_CONFIG_HOME", b.TempDir())
			b.Setenv("XDG_CACHE_HOME", b.TempDir())
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					b.Error(err)
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"id\":\"reply\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			}))
			defer up.Close()
			if err := provider.Save(provider.Provider{ID: "bench", Name: "Bench", Key: "up", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
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
				r, err := http.NewRequest("POST", gw.URL+"/v1/responses", bytes.NewReader(body))
				if err != nil {
					b.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+secret)
				resp, err := client.Do(r)
				if err != nil {
					b.Fatal(err)
				}
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if readErr != nil || resp.StatusCode != 200 {
					b.Fatalf("HTTP %d: %v", resp.StatusCode, readErr)
				}
			}
			call(largeConversation(provider.Responses, "bench/m", 20))
			body := largeConversation(provider.Responses, "bench/m", (mib<<20)/6)
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
			b.SetBytes(int64(len(body)))
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
			b.ReportMetric(float64(len(body))/(1<<20), "request-MiB")
		})
	}
}
