package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// LedgerHealth is safe to publish: no key IDs, credentials or record bodies.
// Pending bytes live in this process. A marker left by another process requires
// reconciliation, never automatic replay that could duplicate an uncertain write.
type LedgerHealth struct {
	Error       string    `json:"error,omitempty"`
	Pending     int       `json:"pending"`
	Bytes       int       `json:"pendingBytes"`
	Uncertain   bool      `json:"uncertain,omitempty"`
	Dropped     int       `json:"dropped,omitempty"`
	MarkerError string    `json:"markerError,omitempty"`
	Since       time.Time `json:"since,omitzero"`
}

type ledgerFault struct {
	path    string
	status  atomic.Pointer[LedgerHealth]
	pending [][]byte // under usage.mu, as are fail, enqueue and RetryLedger
}

var ledgerFaults sync.Map

const pendingLedgerRecords = 1024
const pendingLedgerBytes = 16 << 20

func failureMarker(path string) string { return path + ".write-failed" }

func ledgerState(path string) *ledgerFault {
	if s, ok := ledgerFaults.Load(path); ok {
		return s.(*ledgerFault)
	}
	s := &ledgerFault{path: path}
	h := &LedgerHealth{}
	if _, err := os.Stat(failureMarker(path)); err == nil || !errors.Is(err, os.ErrNotExist) {
		h.Error = "an earlier ledger write failed; reconcile the ledger and failure marker before restarting admission"
		h.Uncertain = true
		if err != nil {
			h.MarkerError = err.Error()
		}
	}
	s.status.Store(h)
	actual, _ := ledgerFaults.LoadOrStore(path, s)
	return actual.(*ledgerFault)
}

// LedgerWriteHealth never waits for a disk write or the append lock after its
// first lookup of this path. It is an admission circuit, not a writable probe.
func LedgerWriteHealth() LedgerHealth { return *ledgerState(Path()).status.Load() }

func (s *ledgerFault) store(h LedgerHealth) { s.status.Store(&h) }

func (s *ledgerFault) publish(h LedgerHealth) {
	s.store(h)
	if h.Error != "" {
		b, _ := json.Marshal(h)
		// The marker is deliberately conservative: even a torn marker blocks
		// the next process. No record is assumed replayable after a restart.
		if err := os.WriteFile(failureMarker(s.path), b, 0600); err != nil {
			h.MarkerError = err.Error()
		} else {
			h.MarkerError = ""
		}
		s.store(h)
	}
}

func (s *ledgerFault) enqueue(line []byte) {
	h := *s.status.Load()
	if len(s.pending) >= pendingLedgerRecords || h.Bytes+len(line) > pendingLedgerBytes {
		h.Dropped++
		h.Uncertain = true
	} else {
		s.pending = append(s.pending, line)
		h.Bytes += len(line)
		h.Pending = len(s.pending)
	}
	s.publish(h)
}

func (s *ledgerFault) fail(line []byte, err error, uncertain bool) {
	h := *s.status.Load()
	first := h.Error == ""
	if first {
		h.Error = err.Error()
		h.Since = time.Now()
	}
	h.Uncertain = h.Uncertain || uncertain
	s.store(h)
	if first {
		log.Printf("gateway accounting unavailable: %v", err)
	}
	if line != nil {
		s.enqueue(line)
	} else {
		s.publish(h)
	}
}

// ledgerWrite is injectable only for short-write/disk-failure regressions.
var ledgerWrite = func(f *os.File, b []byte) (int, error) { return f.Write(b) }

func writeLedgerLine(path string, line []byte) (int, error) {
	f, before, err := openLog(path)
	if err != nil {
		return 0, fmt.Errorf("open usage ledger: %w", err)
	}
	if !holdLog {
		defer f.Close()
	}
	n, err := ledgerWrite(f, line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if holdLog {
			f.Close()
			heldLog.f = nil
		}
		return n, err
	}
	after, err := statLogHandle(f)
	if err != nil {
		return n, err
	}
	noteLogAppend(path, before, after)
	return n, nil
}

// RetryLedger is an explicit operator action, on the serving process. Completed
// writes are removed from pending immediately and never observed/charged again.
// A short write, dropped record or prior-process marker cannot be auto-replayed.
func RetryLedger() error {
	mu.Lock()
	defer mu.Unlock()
	s := ledgerState(Path())
	h := *s.status.Load()
	if h.Error == "" {
		return nil
	}
	if h.Uncertain {
		return errors.New("ledger recovery is uncertain; reconcile missing or partial records and the failure marker offline")
	}
	for len(s.pending) > 0 {
		line := s.pending[0]
		if n, err := writeLedgerLine(s.path, line); err != nil {
			h.Error = err.Error()
			h.Uncertain = n > 0
			s.publish(h)
			return err
		}
		h.Bytes -= len(line)
		s.pending[0] = nil
		s.pending = s.pending[1:]
		h.Pending = len(s.pending)
		s.store(h)
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY, 0600)
	if err == nil {
		err = f.Sync()
		f.Close()
	}
	if err != nil {
		h.Error = err.Error()
		s.publish(h)
		return err
	}
	if err := os.Remove(failureMarker(s.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.Error = err.Error()
		s.publish(h)
		return err
	}
	s.status.Store(&LedgerHealth{})
	return nil
}
