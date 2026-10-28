package budget

import (
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/usage"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type heldLedger struct{ entered, resume chan struct{} }

func (*heldLedger) Serve() func()                   { return func() {} }
func (*heldLedger) Bodies() usage.Bodies            { return usage.NoBodies }
func (*heldLedger) Begin(usage.Request) usage.Watch { return nil }
func (h *heldLedger) Ledger(r usage.Record) {
	if r.CallerKeyID == "slow" {
		close(h.entered)
		<-h.resume
	}
}

func TestSlowSettlementLeavesOtherKeysAvailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	Forget()
	now := time.Now()
	who := access.Identity{KeyID: "fast", Limit: &access.Limit{Period: "day", Tokens: 100}}
	release, refused := Reserve(who, 4, "", now)
	if refused != nil {
		t.Fatal("initial reservation refused")
	}
	release()
	h := &heldLedger{make(chan struct{}), make(chan struct{})}
	remove := usage.Observe(h)
	defer remove()
	done := make(chan struct{})
	go func() { Append(usage.Record{CallerKeyID: "slow", Time: now, Status: 200, Input: 1}); close(done) }()
	<-h.entered
	result := make(chan bool, 1)
	go func() {
		release, refused := Reserve(who, 4, "", now)
		if release != nil {
			release()
		}
		result <- refused == nil
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-result:
		close(h.resume)
		<-done
		if !ok {
			t.Fatal("unrelated key refused")
		}
	case <-timer.C:
		close(h.resume)
		<-done
		<-result
		t.Fatal("one key's ledger write blocked another key's admission")
	}
}

func TestConcurrentReservationsAndSettlement(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	Forget()
	now := time.Now()
	who := access.Identity{KeyID: "one", Limit: &access.Limit{Period: "day", Tokens: 10}}
	var wg sync.WaitGroup
	releases := make(chan func(), 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, refused := Reserve(who, 4, "", now)
			if refused == nil {
				releases <- release
			}
		}()
	}
	wg.Wait()
	close(releases)
	if len(releases) != 10 {
		t.Fatalf("admitted %d reservations, want 10", len(releases))
	}
	for release := range releases {
		Append(usage.Record{CallerKeyID: who.KeyID, Time: now, Status: 200, Input: 1})
		release()
		release()
	}
	if release, refused := Reserve(who, 4, "", now); refused == nil {
		release()
		t.Fatal("settled quota was reused")
	}
	Forget()
	if release, refused := Reserve(who, 4, "", now); refused == nil {
		release()
		t.Fatal("settlement lost after reload")
	}
}
