package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

const accountingPath = "/v1/magpie/accounting"

func accountingAdmin(w http.ResponseWriter, r *http.Request) bool {
	if fromBrowser(r) || !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, 403, "accounting health requires local or shared administrator access")
		return false
	}
	if heldKey(access.Caller(r.Context())) {
		writeError(w, provider.Chat, 403, "this gateway key cannot inspect or retry shared accounting")
		return false
	}
	return true
}

func (s *Server) accountingHealth(w http.ResponseWriter, r *http.Request) {
	if !accountingAdmin(w, r) {
		return
	}
	h := usage.LedgerWriteHealth()
	status := 200
	if h.Error != "" {
		status = 503
	}
	writeJSON(w, status, h)
}

func (s *Server) accountingRetry(w http.ResponseWriter, r *http.Request) {
	if !accountingAdmin(w, r) {
		return
	}
	if err := usage.RetryLedger(); err != nil {
		writeError(w, provider.Chat, 503, err.Error())
		return
	}
	writeJSON(w, 200, usage.LedgerWriteHealth())
}
