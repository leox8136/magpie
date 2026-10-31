package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Cold disk blocks must rebuild the caller index including a child's issuer.
func TestIndexedIssuerAfterDiskRestart(t *testing.T) {
	pageHome(t)
	settleLogClock(t)
	restartLog()
	t.Cleanup(restartLog)
	if err := os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(Path())
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i := 0; i < logBlockRows+1; i++ {
		if err := enc.Encode(Record{Time: time.Now(), CallerKeyID: "child", CallerIssuerID: "parent", Input: 1, Status: 200}); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, restart := range []bool{false, true} {
		if restart {
			restartLog()
		}
		for _, key := range []string{"child", "parent", "unrelated"} {
			n := 0
			err := VisitKeyChecked(key, time.Time{}, func(Record) { n++ })
			want := logBlockRows + 1
			if key == "unrelated" {
				want = 0
			}
			if err != nil || n != want {
				t.Fatalf("restart=%v key=%s: %d want %d, %v", restart, key, n, want, err)
			}
		}
		if _, err := os.Stat(logDiskPath()); err != nil {
			t.Fatal(err)
		}
	}
}
