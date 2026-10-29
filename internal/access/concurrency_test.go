package access

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestCallerConcurrencyValidationAndOwnership(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret, err := Update("add-key", Change{Name: "client"})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := List()
	if err != nil {
		t.Fatal(err)
	}
	id := keys[0].ID
	policy := &Concurrency{MaxConcurrency: 2, QueueLimit: 3}
	if _, err := Update("concurrency-key", Change{Key: id, Concurrency: policy}); err != nil {
		t.Fatal(err)
	}
	policy.MaxConcurrency = 999
	who, ok := Authenticate(secret)
	if !ok || who.Concurrency == nil || who.Concurrency.MaxConcurrency != 2 || who.Concurrency.QueueWait != 3 {
		t.Fatal(who, ok)
	}
	who.Concurrency.MaxConcurrency = 999
	if next, ok := Authenticate(secret); !ok || next.Concurrency.MaxConcurrency != 2 {
		t.Fatal("identity mutated cached policy", next)
	}
	for _, bad := range []Concurrency{{MaxConcurrency: 0}, {MaxConcurrency: -1}, {MaxConcurrency: 1, QueueLimit: -1}, {MaxConcurrency: 1, QueueWait: 301}} {
		if _, err := Update("concurrency-key", Change{Key: id, Concurrency: &bad}); err == nil {
			t.Fatal("invalid policy accepted", bad)
		}
	}
	if _, err := Update("rename-key", Change{Key: id, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if next, ok := Authenticate(secret); !ok || next.Concurrency.MaxConcurrency != 2 {
		t.Fatal("rename lost policy")
	}
	if _, err := Update("concurrency-key", Change{Key: id}); err != nil {
		t.Fatal(err)
	}
	if next, ok := Authenticate(secret); !ok || next.Concurrency != nil {
		t.Fatal("off did not disable policy")
	}
}

func TestCallerConcurrencyNeedsNoLegacyMirrorWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	if err := os.Chmod(settings.Path(), 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Path(), 0600) })
	if err := settings.Save(s); err == nil {
		t.Skip("settings.json remains writable")
	}
	if _, err := Update("concurrency-key", Change{Key: s.LANKeyID, Concurrency: &Concurrency{MaxConcurrency: 2}}); err != nil {
		t.Fatal("concurrency unexpectedly needs to update the legacy credential", err)
	}
	if who, ok := Authenticate(s.LANKey); !ok || who.Concurrency == nil || who.Concurrency.MaxConcurrency != 2 {
		t.Fatal(who, ok)
	}
}
