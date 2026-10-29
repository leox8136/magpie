package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountingCLIUsesServingProcess(t *testing.T) {
	for _, retry := range []bool{false, true} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer magpie" {
				t.Error("missing local token")
			}
			if retry {
				if r.Method != "POST" || r.URL.Path != "/v1/magpie/accounting/retry" {
					t.Error("not a retry")
				}
			} else if r.Method != "GET" || r.URL.Path != "/v1/magpie/accounting" {
				t.Error("not a health read")
			}
			io.WriteString(w, `{"pending":0,"pendingBytes":0}`)
		}))
		var args []string
		if retry {
			args = []string{"retry"}
		}
		var out bytes.Buffer
		err := gatewayAccounting(&out, up.URL, args)
		up.Close()
		if err != nil || !strings.Contains(out.String(), `"pending":0`) {
			t.Fatal(err, out.String())
		}
	}
}
