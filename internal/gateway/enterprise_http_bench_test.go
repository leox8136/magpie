package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// Actual client HTTP -> authenticated gateway Front -> fake upstream HTTP.
// Optional caps are deliberately nonbinding, so throughput counts successful
// requests in both arms rather than confusing rejected requests with more QPS.
func BenchmarkEnterpriseGatewayHTTP(b *testing.B) {
	for _, stream := range []bool{false, true} {
		for _, tenants := range []int{1, 16} {
			for _, policy := range []bool{false, true} {
				b.Run(fmt.Sprintf("stream=%v/tenants=%d/caps=%v", stream, tenants, policy), func(b *testing.B) {
					b.Setenv("XDG_CONFIG_HOME", b.TempDir())
					b.Setenv("XDG_CACHE_HOME", b.TempDir())
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						io.Copy(io.Discard, r.Body)
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
						}
					}))
					defer up.Close()
					if err := provider.Save(provider.Provider{ID: "bench", Name: "Bench", Key: "up", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
						b.Fatal(err)
					}
					var secrets []string
					for i := 0; i < tenants; i++ {
						secret, err := access.Update("add-key", access.Change{Name: fmt.Sprint("client-", i)})
						if err != nil {
							b.Fatal(err)
						}
						secrets = append(secrets, secret)
					}
					if policy {
						data, err := os.ReadFile(access.Path())
						if err != nil {
							b.Fatal(err)
						}
						var rows []map[string]any
						if err := json.Unmarshal(data, &rows); err != nil {
							b.Fatal(err)
						}
						for _, r := range rows {
							r["concurrency"] = map[string]any{"maxConcurrency": 128, "queueLimit": 16, "queueWait": 3}
						}
						data, err = json.Marshal(rows)
						if err != nil {
							b.Fatal(err)
						}
						if err := os.WriteFile(access.Path(), data, 0600); err != nil {
							b.Fatal(err)
						}
					}
					gw := httptest.NewServer(New().Front())
					defer gw.Close()
					transport := &http.Transport{MaxIdleConnsPerHost: 64}
					defer transport.CloseIdleConnections()
					client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
					body := []byte(fmt.Sprintf(`{"model":"bench/m","stream":%v,"messages":[{"role":"user","content":"%s"}]}`, stream, strings.Repeat("hello ", 200)))
					var sequence atomic.Uint64
					var rejected atomic.Int64
					var samplesMu sync.Mutex
					var samples []time.Duration
					beforeG := runtime.NumGoroutine()
					b.SetParallelism(2)
					b.ReportAllocs()
					b.ResetTimer()
					began := time.Now()
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							id := sequence.Add(1)
							r, err := http.NewRequest("POST", gw.URL+"/v1/chat/completions", bytes.NewReader(body))
							if err != nil {
								b.Error(err)
								continue
							}
							r.Header.Set("Authorization", "Bearer "+secrets[(id-1)%uint64(len(secrets))])
							at := time.Now()
							resp, err := client.Do(r)
							if err != nil {
								b.Error(err)
								continue
							}
							_, err = io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
							if err != nil {
								b.Error(err)
							}
							if resp.StatusCode != 200 {
								rejected.Add(1)
							}
							d := time.Since(at)
							samplesMu.Lock()
							samples = append(samples, d)
							samplesMu.Unlock()
						}
					})
					elapsed := time.Since(began)
					b.StopTimer()
					if rejected.Load() != 0 {
						b.Fatalf("%d rejected requests cannot count as throughput", rejected.Load())
					}
					slices.Sort(samples)
					if len(samples) > 0 {
						for _, pct := range []int{50, 95, 99} {
							i := min(len(samples)-1, (len(samples)-1)*pct/100)
							b.ReportMetric(float64(samples[i])/float64(time.Millisecond), fmt.Sprintf("p%d-ms", pct))
						}
					}
					b.ReportMetric(float64(b.N)/elapsed.Seconds(), "success/s")
					b.ReportMetric(float64(runtime.NumGoroutine()-beforeG), "goroutines-after")
				})
			}
		}
	}
}
