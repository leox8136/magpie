package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/gwbudget"
)

func TestBudgetBodySharesAdmissionAndBytes(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "zstd", "chunked"} {
		t.Run(encoding, func(t *testing.T) {
			fresh(t)
			budget.Forget()
			old := bodyRoom
			room := roomOf(8 << 20)
			bodyRoom = room
			t.Cleanup(func() { bodyRoom = old })
			remove := gwbudget.Limit(budget.Limiter{})
			defer remove()
			plain := []byte(`{"model":"test/m","input":"` + strings.Repeat("x", 2<<20) + `"}`)
			wire := encodedBody(t, plain, encoding)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			who := access.Identity{KeyID: "body", Limit: &access.Limit{Period: "day", Cost: 100000}}
			r := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(wire)).WithContext(access.WithIdentity(ctx, who))
			if encoding == "chunked" {
				r.ContentLength = -1
			} else {
				r.Header.Set("Content-Encoding", encoding)
			}
			s := New()
			h := keyLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if held := roomHeld(room); held < int64(len(plain)) {
					t.Errorf("budget pre-read held %d bytes, want at least %d", held, len(plain))
				}
				first, ok := s.requestBody(w, r, "responses")
				if !ok {
					t.Error("main read failed")
					return
				}
				second, status, err := readBoundedRequestBody(w, r, requestLimits{})
				if err != nil || status != 0 || !bytes.Equal(first, plain) || !bytes.Equal(second, plain) {
					t.Errorf("repeated read: lengths %d/%d, status %d, %v", len(first), len(second), status, err)
					return
				}
				if &first[0] != &second[0] {
					t.Error("read copied the decoded body")
				}
				_, status, _ = readBoundedRequestBody(w, r, requestLimits{body: 1024})
				if status != 413 {
					t.Errorf("cached body bypassed a stricter limit: %d", status)
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), r)
			cancel()
			until := time.Now().Add(5 * time.Second)
			for roomHeld(room) != 0 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if roomHeld(room) != 0 {
				t.Fatal("request kept admission after cancellation")
			}
		})
	}
}

func TestDecodedBodyGrowthRespectsRoom(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd", "chunked"} {
		t.Run(encoding, func(t *testing.T) {
			old := bodyRoom
			room := roomOf(4 << 20)
			bodyRoom = room
			defer func() { bodyRoom = old }()
			busy, endBusy := context.WithCancel(context.Background())
			defer endBusy()
			if err := room.take(busy, 3<<20); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wire := encodedBody(t, bytes.Repeat([]byte("x"), 3<<20), encoding)
			r := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(wire)).WithContext(ctx)
			if encoding == "chunked" {
				r.ContentLength = -1
			} else {
				r.Header.Set("Content-Encoding", encoding)
			}
			_, status, err := readBoundedRequestBody(httptest.NewRecorder(), r, requestLimits{})
			if status != 503 || err == nil {
				t.Fatalf("decoded into occupied room: status %d, %v", status, err)
			}
			if held := roomHeld(room); held > 4<<20 {
				t.Fatalf("room exceeded: %d", held)
			}
		})
	}
}

func TestGrowingBodyLeavesQueuedRequestsFirst(t *testing.T) {
	room := roomOf(4 << 20)
	busy, endBusy := context.WithCancel(context.Background())
	wait, endWait := context.WithCancel(context.Background())
	grow, endGrow := context.WithCancel(context.Background())
	defer endBusy()
	defer endWait()
	defer endGrow()
	if err := room.take(busy, 3<<20); err != nil {
		t.Fatal(err)
	}
	queued := taking(room, wait, 2<<20)
	waitQueued(t, room, 1)
	if err := room.grow(grow, 1<<20); err == nil {
		t.Error("new decoder overtook queued request")
	}
	endBusy()
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
}

func BenchmarkBudgetRequestBody(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20} {
		for _, encoding := range []string{"identity", "gzip", "chunked"} {
			b.Run(sizeWord(int64(size))+"/"+encoding, func(b *testing.B) {
				b.Setenv("XDG_CONFIG_HOME", b.TempDir())
				budget.Forget()
				remove := gwbudget.Limit(budget.Limiter{})
				defer remove()
				old := bodyRoom
				bodyRoom = roomOf(0)
				defer func() { bodyRoom = old }()
				plain := []byte(`{"model":"test/m","input":"` + strings.Repeat("x", size) + `"}`)
				wire := plain
				if encoding == "gzip" {
					var buf bytes.Buffer
					z := gzip.NewWriter(&buf)
					z.Write(plain)
					z.Close()
					wire = buf.Bytes()
				}
				who := access.Identity{KeyID: "bench", Limit: &access.Limit{Period: "day", Cost: 1e9}}
				s := New()
				h := keyLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, ok := s.requestBody(w, r, "responses")
					if !ok || len(body) != len(plain) {
						b.Fatal("body read failed")
					}
				}))
				b.ReportAllocs()
				b.SetBytes(int64(len(plain)))
				b.ResetTimer()
				for range b.N {
					ctx, cancel := context.WithCancel(context.Background())
					r := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(wire)).WithContext(access.WithIdentity(ctx, who))
					if encoding == "chunked" {
						r.ContentLength = -1
					} else {
						r.Header.Set("Content-Encoding", encoding)
					}
					h.ServeHTTP(httptest.NewRecorder(), r)
					r.Body.Close()
					cancel()
				}
			})
		}
	}
}
