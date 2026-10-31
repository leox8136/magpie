package gateway

import (
	"github.com/yetone/magpie/internal/access"
	"testing"
	"time"
)

func TestPrunedExpiryFailsClosed(t *testing.T) {
	fresh(t)
	now := time.Now()
	old := access.Now
	access.Now = func() time.Time { return now }
	defer func() { access.Now = old }()
	k, err := access.IssueKey(access.Issue{TTL: time.Minute, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Front()
	now = now.Add(2 * time.Hour)
	before := keysCall(h, "POST", KeysPath, fromHere, k.Key, `{"ttl":"1h"}`)
	if before.Code != 401 {
		t.Fatalf("fixture: expired key not refused: %d", before.Code)
	}
	if n, err := access.Prune(); err != nil || n != 1 {
		t.Fatalf("prune %d %v", n, err)
	}
	after := keysCall(h, "POST", KeysPath, fromHere, k.Key, `{"ttl":"1h"}`)
	if after.Code != 401 {
		t.Fatalf("pruned expired read-only key gained local admin issuance: status=%d", after.Code)
	}
}
