package access

import (
	"slices"
	"testing"
)

// A key that names accounts and no model may use only its accounts'
// providers' models (Linka on Discord); one that lists models keeps
// them, and one that lists neither uses every model.
func TestHeldModels(t *testing.T) {
	for _, c := range []struct {
		models, accounts, want []string
	}{
		{nil, nil, nil},
		{[]string{"openai/gpt-5"}, []string{"codex/acct-1"}, []string{"openai/gpt-5"}},
		{nil, []string{"codex/acct-1", "CODEX/acct-2", "relay/abc"}, []string{"codex/*", "relay/*"}},
	} {
		if got := HeldModels(c.models, c.accounts); !slices.Equal(got, c.want) {
			t.Errorf("HeldModels(%v, %v) = %v, want %v", c.models, c.accounts, got, c.want)
		}
	}
	who := Identity{Models: HeldModels(nil, []string{"codex/acct-1"}), Accounts: []string{"codex/acct-1"}}
	if !who.Allows("codex/gpt-5.5") || who.Allows("anthropic/claude-opus-4-8") || who.Allows("group/any") {
		t.Fatal("an account-only key's models", who.Models)
	}
}

// Authentication must derive restrictions on both prepared and reused key
// snapshots, without persisting derived model picks or losing other limits.
func TestAccountsOnlyAuthenticationKeepsOwnedCurrentPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret := Prefix + "account-scope"
	key := Key{ID: "tenant", Secret: secret, Accounts: []string{"a/one"}, Limit: &Limit{Tokens: 100}, Concurrency: &Concurrency{MaxConcurrency: 2}}
	if err := Restore([]Key{key}); err != nil {
		t.Fatal(err)
	}
	check := func(allowed, barred string) {
		t.Helper()
		who, ok := Authenticate(secret)
		if !ok || !who.Allows(allowed) || who.Allows(barred) || who.Limit == nil || who.Limit.Tokens != 100 || who.Concurrency == nil || who.Concurrency.MaxConcurrency != 2 {
			t.Fatalf("wrong cached permissions: %+v", who)
		}
		who.Models[0] = "*"
	}
	check("a/m", "b/m")
	check("a/m", "b/m")
	key.Accounts = []string{"b/two"}
	if err := Restore([]Key{key}); err != nil {
		t.Fatal(err)
	}
	check("b/m", "a/m")
	keys, err := List()
	if err != nil || len(keys[0].Models) != 0 {
		t.Fatal("derived models were persisted", keys, err)
	}
	if _, err := Update("models-key", Change{Key: "tenant", Models: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	check("a/m", "b/m")
}
