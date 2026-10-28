package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestVisitKeyTracksLedgerChanges(t *testing.T) {
	pageHome(t)
	now := time.Now()
	for i := 0; i < 1030; i++ {
		Append(Record{Time: now.Add(time.Duration(i-515) * time.Second), CallerKeyID: fmt.Sprint(i % 3), Input: i + 1})
	}
	check := func() {
		t.Helper()
		for _, key := range []string{"0", "1", "2", "", "missing"} {
			for _, since := range []time.Time{{}, now} {
				var want, got []Record
				Visit(since, func(r Record) {
					if r.CallerKeyID == key {
						want = append(want, r)
					}
				})
				VisitKey(key, since, func(r Record) { got = append(got, r) })
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("key %q: indexed records differ", key)
				}
			}
		}
	}
	check()
	Append(Record{Time: now, CallerKeyID: "0", Input: 99})
	check()
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte(`"callerKeyId":"0"`), []byte(`"callerKeyId":"2"`))
	replacement := Path() + ".replacement"
	if err := os.WriteFile(replacement, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, Path()); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.WriteFile(Path(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	check()
}

func keyIndexFixture(n int) *logSnapshot {
	s := &logSnapshot{}
	for block := 0; block < n; block++ {
		c := &rowChunk{}
		for i := 0; i < logBlockRows; i++ {
			c.add(Row{Record: Record{Time: time.Unix(1000, 0), CallerKeyID: fmt.Sprint(block*logBlockRows + i), Input: 1}}, "", 0, false)
		}
		s.callers = append(s.callers, blockCallers(c))
		s.blocks = append(s.blocks, c.freeze())
	}
	return s
}

func TestVisitKeySkipsUnrelatedBlocks(t *testing.T) {
	s := keyIndexFixture(200)
	count := 0
	allocs := testing.AllocsPerRun(1, func() {
		count = 0
		s.visitKey("42", time.Time{}, func(r Record) {
			count++
			if r.CallerKeyID != "42" {
				t.Fatal("wrong caller")
			}
		})
	})
	if count != 1 {
		t.Fatalf("got %d records", count)
	}
	// One matching block needs a few buffers; decoding 200 unrelated blocks
	// allocates thousands. Leave ample room for decoder changes.
	if allocs > 100 {
		t.Fatalf("single-key read decoded unrelated history: %.0f allocations", allocs)
	}
}

func BenchmarkVisitKey(b *testing.B) {
	s := keyIndexFixture(200)
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("indexed=%v", indexed), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				n := 0
				fn := func(r Record) {
					if r.CallerKeyID == "42" {
						n++
					}
				}
				if indexed {
					s.visitKey("42", time.Time{}, fn)
				} else {
					s.visit(time.Time{}, fn)
				}
				if n != 1 {
					b.Fatal(n)
				}
			}
		})
	}
}

func BenchmarkVisitKeyLedger(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for i := 0; i < 100000; i++ {
		if err := enc.Encode(Record{Time: time.Unix(1000, 0), CallerKeyID: fmt.Sprint(i), Input: 1}); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(Path(), data.Bytes(), 0600); err != nil {
		b.Fatal(err)
	}
	VisitKey("42", time.Time{}, func(Record) {})
	b.ResetTimer()
	for b.Loop() {
		VisitKey("42", time.Time{}, func(Record) {})
	}
}
