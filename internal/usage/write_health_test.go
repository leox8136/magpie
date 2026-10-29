package usage

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLedgerRetryWritesEachRecordOnce(t *testing.T) {
	pageHome(t)
	original := ledgerWrite
	ledgerWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") }
	t.Cleanup(func() { ledgerWrite = original })
	Append(Record{Time: time.Now(), CallerKeyID: "one", Input: 11})
	Append(Record{Time: time.Now(), CallerKeyID: "one", Input: 12})
	h := LedgerWriteHealth()
	if h.Error == "" || h.Pending != 2 || h.Uncertain {
		t.Fatalf("health: %+v", h)
	}
	if _, err := os.Stat(failureMarker(Path())); err != nil {
		t.Fatal("no restart marker", err)
	}
	ledgerWrite = original
	if err := RetryLedger(); err != nil {
		t.Fatal(err)
	}
	if err := RetryLedger(); err != nil {
		t.Fatal(err)
	}
	if h := LedgerWriteHealth(); h.Error != "" || h.Pending != 0 {
		t.Fatal(h)
	}
	n, total := 0, 0
	if err := VisitKeyChecked("one", time.Time{}, func(r Record) { n++; total += r.Input }); err != nil {
		t.Fatal(err)
	}
	if n != 2 || total != 23 {
		t.Fatalf("replayed %d records, input %d", n, total)
	}
	if _, err := os.Stat(failureMarker(Path())); !os.IsNotExist(err) {
		t.Fatalf("marker not removed: %v", err)
	}
}

func TestLedgerShortWriteNeverBlindlyReplayed(t *testing.T) {
	pageHome(t)
	write := ledgerWrite
	ledgerWrite = func(f *os.File, b []byte) (int, error) {
		n, err := f.Write(b[:5])
		if err != nil {
			return n, err
		}
		return n, errors.New("short disk write")
	}
	defer func() { ledgerWrite = write }()
	Append(Record{CallerKeyID: "one", Input: 11})
	ledgerWrite = write
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := RetryLedger(); err == nil {
		t.Fatal("partial write was replayed")
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !LedgerWriteHealth().Uncertain {
		t.Fatal("uncertain ledger changed")
	}
	// A fresh process sees only the marker, never an empty healthy queue.
	ledgerFaults.Delete(Path())
	if h := LedgerWriteHealth(); h.Error == "" || !h.Uncertain {
		t.Fatalf("restart forgot failure: %+v", h)
	}
}

func TestLedgerRetryResumesAfterConfirmedWrites(t *testing.T) {
	pageHome(t)
	write := ledgerWrite
	defer func() { ledgerWrite = write }()
	ledgerWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") }
	Append(Record{CallerKeyID: "one", Input: 1})
	Append(Record{CallerKeyID: "one", Input: 2})
	tries := 0
	ledgerWrite = func(f *os.File, b []byte) (int, error) {
		tries++
		if tries == 2 {
			return 0, errors.New("full again")
		}
		return write(f, b)
	}
	if err := RetryLedger(); err == nil || LedgerWriteHealth().Pending != 1 {
		t.Fatal("retry did not retain only the failed record", err, LedgerWriteHealth())
	}
	ledgerWrite = write
	if err := RetryLedger(); err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := VisitKeyChecked("one", time.Time{}, func(Record) { n++ }); err != nil || n != 2 {
		t.Fatalf("duplicated completed retry: %d %v", n, err)
	}
}

func TestLedgerPendingQueueIsBounded(t *testing.T) {
	pageHome(t)
	write := ledgerWrite
	ledgerWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") }
	defer func() { ledgerWrite = write }()
	for i := 0; i < pendingLedgerRecords+2; i++ {
		Append(Record{CallerKeyID: "one", Input: i})
	}
	h := LedgerWriteHealth()
	if h.Pending != pendingLedgerRecords || h.Dropped != 2 || !h.Uncertain {
		t.Fatalf("unbounded queue: %+v", h)
	}
	if err := RetryLedger(); err == nil {
		t.Fatal("lost records were treated as recovered")
	}
}

func TestLedgerEncodingFailureTripsCircuit(t *testing.T) {
	pageHome(t)
	bad := math.NaN()
	Append(Record{CallerKeyID: "one", OwnCost: &bad})
	if h := LedgerWriteHealth(); !h.Uncertain || !strings.Contains(h.Error, "encode accounting record") {
		t.Fatalf("encoding failure lost: %+v", h)
	}
}

func TestLedgerHealthDoesNotWaitForAppend(t *testing.T) {
	pageHome(t)
	LedgerWriteHealth() // initialize the path before holding its write
	write := ledgerWrite
	entered, gate, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ledgerWrite = func(f *os.File, b []byte) (int, error) {
		close(entered)
		<-gate
		return write(f, b)
	}
	go func() { defer close(done); Append(Record{Input: 1}) }()
	defer func() { close(gate); <-done; ledgerWrite = write }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("append did not start")
	}
	health := make(chan LedgerHealth, 1)
	go func() { health <- LedgerWriteHealth() }()
	select {
	case h := <-health:
		if h.Error != "" {
			t.Fatal(h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health waits on the append lock")
	}
}
