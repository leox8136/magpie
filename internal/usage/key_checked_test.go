package usage

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestCheckedKeyReadRejectsUnknownHashAndRecovers(t *testing.T) {
	pageHome(t)
	Append(Record{CallerKeyID: "one", Input: 10})
	want := recordHash(Path(), mustStat(t).Size())
	original := logRecordHash
	logRecordHash = func(string, int64) string { return "" }
	t.Cleanup(func() { logRecordHash = original })
	logIndex.Lock()
	logIndex.snapshot = nil
	logIndex.Unlock()
	calls := 0
	if err := VisitKeyChecked("one", time.Time{}, func(Record) { calls++ }); err == nil || calls != 0 {
		t.Fatalf("failed fingerprint: %v, %d callbacks", err, calls)
	}
	logRecordHash = original
	if err := VisitKeyChecked("one", time.Time{}, func(Record) { calls++ }); err != nil || calls != 1 {
		t.Fatalf("repaired fingerprint: %v, %d callbacks", err, calls)
	}
	if got := readLogSnapshot().hash; got != want {
		t.Fatal("failed read replaced the ledger")
	}
	if runtime.GOOS == "windows" {
		return
	} // chmod does not revoke Windows read access
	if err := os.Chmod(Path(), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(Path(), 0600) })
	if err := VisitKeyChecked("one", time.Time{}, func(Record) { t.Error("unreadable cached ledger yielded records") }); err == nil {
		t.Fatal("unreadable cached ledger accepted")
	}
}
