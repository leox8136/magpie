package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/usage"
)

func TestFailedAccountingBlocksWarmBudget(t *testing.T) {
	fresh(t)
	now := pinLimitClock(t)
	budget.Forget()
	limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Capped", "Open")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 100000})
	gw := httptest.NewServer(New().Front())
	defer gw.Close()
	call := func(secret string) int {
		t.Helper()
		r, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(chatReq))
		r.Header.Set("Authorization", "Bearer "+secret)
		res, err := gw.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	if got := call(secrets[0]); got != 200 {
		t.Fatal(got)
	}
	path := usage.Path()
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("first call was not recorded")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(path + ".saved"); err == nil {
			os.Remove(path)
			os.Rename(path+".saved", path)
		}
	})
	if got := call(secrets[0]); got != 200 {
		t.Fatalf("finished reply was changed by accounting: %d", got)
	}
	if got := call(secrets[0]); got != 503 {
		t.Fatalf("warm budget admitted after a failed write: %d", got)
	}
	if got := call(secrets[1]); got != 200 {
		t.Fatalf("unbudgeted request was stopped: %d", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secrets[0], "magpie"} {
		r, _ := http.NewRequest("POST", gw.URL+"/v1/magpie/accounting/retry", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		res, err := gw.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if secret == secrets[0] && res.StatusCode != 403 {
			t.Fatalf("limited key recovered shared accounting: %d", res.StatusCode)
		}
		if secret == "magpie" && res.StatusCode != 200 {
			t.Fatalf("retry failed: %d", res.StatusCode)
		}
	}
	if got := call(secrets[0]); got != 200 {
		t.Fatalf("repaired budget did not reopen: %d", got)
	}
	if st := budget.Of(access.Key{ID: keys[0].ID, Limit: &access.Limit{Period: "day", Tokens: 100000}}, now); st == nil || st.Calls != 3 {
		t.Fatalf("retry lost or doubled consumption: %+v", st)
	}
}
