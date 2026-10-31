package access

import (
	"errors"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestIssuedAccountIntersectionAfterParentChange(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "openai", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-a", Name: "a"}, {Key: "sk-b", Name: "b"}}}); err != nil {
		t.Fatal(err)
	}
	id, _ := addKey(t, "parent")
	for _, c := range []struct {
		action string
		change Change
	}{
		{"issuer-key", Change{Key: id, Issues: true}},
		{"accounts-key", Change{Key: id, Accounts: []string{"openai/a", "openai/b"}}},
	} {
		if _, err := Update(c.action, c.change); err != nil {
			t.Fatal(err)
		}
	}
	child, err := IssueKey(Issue{TTL: time.Hour, Issuer: id, Accounts: []string{"openai/a"}})
	if err != nil {
		t.Fatal(err)
	}
	unrestricted, err := IssueKey(Issue{TTL: time.Hour, Issuer: id})
	if err != nil {
		t.Fatal(err)
	}
	who, err := Check(child.Key)
	if err != nil || !who.AllowsAccount("openai", provider.KeyID("sk-a")) || who.AllowsAccount("openai", provider.KeyID("sk-b")) {
		t.Fatalf("child's original restriction: %v %v", who.Accounts, err)
	}
	if _, err = Update("accounts-key", Change{Key: id, Accounts: []string{"openai/b"}}); err != nil {
		t.Fatal(err)
	}
	_, err = Check(child.Key)
	var refused *Refused
	if !errors.As(err, &refused) {
		t.Fatalf("disjoint account restrictions were not refused: %v", err)
	}
	who, err = Check(unrestricted.Key)
	if err != nil || !who.AllowsAccount("openai", provider.KeyID("sk-b")) || who.AllowsAccount("openai", provider.KeyID("sk-a")) {
		t.Fatalf("child without its own restriction should inherit B: %v %v", who.Accounts, err)
	}
}
