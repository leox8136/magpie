package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

// Caller admission precedes budget reservation and body reads. A busy caller
// holds neither another caller's permit nor budget while waiting. Bridge tool
// callbacks bypass it, since they may be needed to finish the held request.
func (s *Server) callerLimited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := access.Caller(r.Context())
		if who.KeyID == "" || who.Concurrency == nil || r.Method != http.MethodPost || r.URL.Path == accountingPath+"/retry" || strings.HasPrefix(r.URL.Path, "/_magpie/") {
			next.ServeHTTP(w, r)
			return
		}
		policy, err := who.Concurrency.Valid()
		if err != nil {
			writeError(w, protoOfPath(r.URL.Path), 503, "this key's concurrency policy is invalid")
			return
		}
		if r.Context().Err() != nil {
			return
		}
		endPhase := notePhase(r.Context(), "caller")
		release, _, err := s.callerLanes.takeQueue(r.Context(), who.KeyID, policy.MaxConcurrency, policy.QueueLimit, time.Duration(policy.QueueWait)*time.Second, true)
		endPhase()
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			msg := fmt.Sprintf("gateway key %q is at its concurrency limit (%d active, at most %d waiting): %v", who.KeyName, policy.MaxConcurrency, policy.QueueLimit, err)
			w.Header().Set("Connection", "close")
			controller := http.NewResponseController(w)
			controller.SetReadDeadline(time.Now())
			if r.Body != nil {
				r.Body.Close()
			}
			controller.SetReadDeadline(time.Time{})
			w.Header().Set("Retry-After", "1")
			w.Header().Set("X-Should-Retry", "false")
			writeError(w, protoOfPath(r.URL.Path), 429, msg)
			c := callerOf(r)
			appendUsage(r, usage.Record{Time: time.Now(), Agent: c.agent, Via: c.via, Status: 429, Rejected: true, Error: msg, ErrType: "gateway_key_concurrency", Endpoint: r.URL.Path})
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}
