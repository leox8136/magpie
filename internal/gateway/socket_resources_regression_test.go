package gateway

import (
	"github.com/yetone/magpie/internal/access"
	"net/http/httptest"
	"testing"
	"time"
)

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
