package budget

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
)

func TestUnreadableBudgetNeverStartsAtZero(t *testing.T) {
	for _, broken := range []string{"directory", "permission", "malformed", "partial"} {
		t.Run(broken, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			Forget()
			path := usage.Path()
			os.MkdirAll(filepath.Dir(path), 0700)
			switch broken {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "permission":
				if runtime.GOOS == "windows" {
					t.Skip("Windows chmod does not revoke read access")
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(path, 0600) })
			case "malformed":
				os.WriteFile(path, []byte("broken\n"), 0600)
			case "partial":
				os.WriteFile(path, []byte(`{"callerKeyId":"one","in":100`), 0600)
			}
			now := time.Now()
			who := access.Identity{KeyID: "one", Limit: &access.Limit{Period: "day", Tokens: 10}}
			release, refused := Reserve(who, 4, "", now)
			if release != nil {
				release()
			}
			if refused == nil {
				t.Fatal("unreadable or incomplete ledger was admitted as zero usage")
			}
			if broken == "directory" {
				os.Remove(path)
			}
			os.Chmod(path, 0600)
			if err := os.WriteFile(path, []byte(""), 0600); err != nil {
				t.Fatal(err)
			}
			release, refused = Reserve(who, 4, "", now)
			if refused != nil {
				t.Fatalf("repaired ledger still refused: %v", refused)
			}
			release()
		})
	}
}

func budgetWindowFixture(tb testing.TB, n int) time.Time {
	tb.Helper()
	tb.Setenv("XDG_CONFIG_HOME", tb.TempDir())
	Forget()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	price := 0.01
	for i := 0; i < n; i++ {
		if err := enc.Encode(usage.Record{Time: now.Add(-time.Duration(i%240) * time.Hour), CallerKeyID: "one", Input: 10, Output: 2, CacheRead: 3, Status: 200, OwnCost: &price}); err != nil {
			tb.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0700); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(usage.Path(), data.Bytes(), 0600); err != nil {
		tb.Fatal(err)
	}
	usage.VisitKey("one", time.Time{}, func(usage.Record) {})
	return now
}

func TestBudgetWindowsMatchIndependentReads(t *testing.T) {
	now := budgetWindowFixture(t, 2200)
	who := access.Identity{KeyID: "one", Limit: &access.Limit{Period: "day", Tokens: 1e9}, More: []access.Limit{{Period: "week", Tokens: 1e9, CacheReads: true}, {Period: "month", Tokens: 1e9}, {Period: "day", Cost: 1e9}}}
	for round := 0; round < 2; round++ {
		release, refused := Reserve(who, 4, "", now)
		if refused != nil {
			t.Fatal(refused)
		}
		release()
		for _, st := range All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, now) {
			var calls int
			var tokens int64
			var cost float64
			usage.VisitKey("one", st.Start, func(r usage.Record) {
				if counts(r) {
					calls++
					tokens += int64(r.Input + r.Output + r.CacheWrite)
					if st.CacheReads {
						tokens += int64(r.CacheRead)
					}
					if r.OwnCost != nil {
						cost += *r.OwnCost
					}
				}
			})
			if st.Calls != calls || st.Tokens != tokens || st.Cost != cost {
				t.Fatalf("%s mismatch: %+v, want %d/%d/%v", st.Period, st, calls, tokens, cost)
			}
		}
		Append(usage.Record{Time: now, CallerKeyID: "one", Input: 9, Status: 200})
	}
}

func BenchmarkBudgetColdWindows(b *testing.B) {
	for _, many := range []bool{false, true} {
		name := "one"
		if many {
			name = "three"
		}
		b.Run(name, func(b *testing.B) {
			now := budgetWindowFixture(b, 30000)
			who := access.Identity{KeyID: "one", Limit: &access.Limit{Period: "day", Tokens: 1e12}}
			if many {
				who.More = []access.Limit{{Period: "week", Tokens: 1e12}, {Period: "month", Tokens: 1e12}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				Forget()
				release, refused := Reserve(who, 4, "", now)
				if refused != nil {
					b.Fatal(refused)
				}
				release()
			}
		})
	}
}
