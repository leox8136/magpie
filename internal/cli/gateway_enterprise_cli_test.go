package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

func TestGatewayCallerConcurrencyCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := access.Update("add-key", access.Change{Name: "tenant"}); err != nil {
		t.Fatal(err)
	}
	keys, err := access.List()
	if err != nil {
		t.Fatal(err)
	}
	id := keys[0].ID
	for _, args := range [][]string{{"--max", "2", "--queue", "3", "--wait", "1"}, {"off"}} {
		var out bytes.Buffer
		if err := gatewayKeysTo(&out, append([]string{"gateway-key", "concurrency", id}, args...)); err != nil {
			t.Fatal(err)
		}
		keys, err = access.List()
		if err != nil {
			t.Fatal(err)
		}
		if args[0] == "off" {
			if keys[0].Concurrency != nil {
				t.Fatal("still limited")
			}
		} else if keys[0].Concurrency == nil || keys[0].Concurrency.MaxConcurrency != 2 || keys[0].Concurrency.QueueLimit != 3 {
			t.Fatal(keys[0])
		}
	}
	for _, args := range [][]string{{"--max", "-1"}, {"--queue", "1"}, {"--max", "1", "--max", "2"}, {"--max"}} {
		if err := gatewayKeyConcurrency(io.Discard, append([]string{id}, args...)); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
}

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
