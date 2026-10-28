package access

import (
	"bytes"
	"fmt"
	"github.com/yetone/magpie/internal/edit"
	"os"
	"sync"
	"testing"
	"time"
)

func TestAuthenticationReturnsOwnedRestrictions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	k := Key{ID: "one", Secret: Prefix + "test", Limit: &Limit{Tokens: 100}, Models: []string{"vendor/model"}, More: []Limit{{Tokens: 200}}, Accounts: []string{"vendor/account"}}
	if err := Restore([]Key{k}); err != nil {
		t.Fatal(err)
	}
	a, ok := Authenticate(k.Secret)
	if !ok {
		t.Fatal("key refused")
	}
	a.Limit.Tokens = 0
	a.Models[0] = "*"
	a.More[0].Tokens = 0
	a.Accounts[0] = "*"
	b, ok := Authenticate(k.Secret)
	if !ok || b.Limit.Tokens != 100 || b.Models[0] != "vendor/model" || b.More[0].Tokens != 200 || b.Accounts[0] != "vendor/account" {
		t.Fatal("caller mutated cached permissions")
	}
}

func TestAuthenticationSeesSameStampReplacement(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, second := Prefix+"aaaa", Prefix+"bbbb"
	if err := Restore([]Key{{ID: "one", Secret: first}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(first); !ok {
		t.Fatal("key refused")
	}
	if err := Restore([]Key{{ID: "one", Secret: second}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(Path(), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(first); ok {
		t.Fatal("rotated key accepted")
	}
	if _, ok := Authenticate(second); !ok {
		t.Fatal("new key refused")
	}
	if err := os.WriteFile(Path(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(second); ok {
		t.Fatal("corrupt store used cached permissions")
	}
}

func TestAuthenticationDoesNotWaitForStoreWriter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret := Prefix + "test"
	if err := Restore([]Key{{ID: "one", Secret: secret}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(secret); !ok {
		t.Fatal("key refused")
	}
	mu.Lock()
	result := make(chan bool, 1)
	go func() { _, ok := Authenticate(secret); result <- ok }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-result:
		mu.Unlock()
		if !ok {
			t.Fatal("key refused while writer prepares atomic replacement")
		}
	case <-timer.C:
		mu.Unlock()
		<-result
		t.Fatal("authentication waited for an unrelated store writer")
	}
}

func BenchmarkAuthenticateKeyCount(b *testing.B) {
	for _, n := range []int{1, 1000, 10000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.Setenv("XDG_CONFIG_HOME", b.TempDir())
			keys := make([]Key, n)
			for i := range keys {
				keys[i] = Key{ID: fmt.Sprint(i), Name: "Benchmark", Secret: fmt.Sprintf("%s%024d", Prefix, i)}
			}
			if err := Restore(keys); err != nil {
				b.Fatal(err)
			}
			secret := keys[n-1].Secret
			if _, ok := Authenticate(secret); !ok {
				b.Fatal("key refused")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, ok := Authenticate(secret); !ok {
					b.Fatal("key refused")
				}
			}
		})
	}
}

func TestAuthenticationSeesInPlaceRewriteAndReadFailures(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret := Prefix + "original"
	if err := Restore([]Key{{ID: "one", Secret: secret, Name: "first"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(secret); !ok {
		t.Fatal("key refused")
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("first"), []byte("other"))
	if err := os.WriteFile(Path(), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(Path(), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if who, ok := Authenticate(secret); !ok || who.KeyName != "other" {
		t.Fatal("same-size rewrite returned stale identity")
	}
	if err := os.Chmod(Path(), 0000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(Path()); err == nil {
		f.Close()
	} else if _, ok := Authenticate(secret); ok {
		t.Fatal("unreadable store accepted a cached key")
	}
	if err := os.Chmod(Path(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(Path()); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(secret); ok {
		t.Fatal("deleted store accepted a cached key")
	}
}

func TestAuthenticationConcurrentRotation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, second := Prefix+"first", Prefix+"other"
	if err := Restore([]Key{{ID: "one", Secret: first}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					Authenticate(first)
					Authenticate(second)
				}
			}
		}()
	}
	for i := range 20 {
		current, old := first, second
		if i%2 == 0 {
			current, old = second, first
		}
		if err := Restore([]Key{{ID: "one", Secret: current}}); err != nil {
			close(stop)
			wg.Wait()
			t.Fatal(err)
		}
		if _, ok := Authenticate(old); ok {
			close(stop)
			wg.Wait()
			t.Fatal("old key accepted after completed replacement")
		}
		if _, ok := Authenticate(current); !ok {
			close(stop)
			wg.Wait()
			t.Fatal("new key refused after completed replacement")
		}
	}
	close(stop)
	wg.Wait()
}

func BenchmarkKeyUpdateAndReauthentication(b *testing.B) {
	for _, prepared := range []bool{false, true} {
		b.Run(fmt.Sprintf("prepared=%v", prepared), func(b *testing.B) {
			b.Setenv("XDG_CONFIG_HOME", b.TempDir())
			keys := make([]Key, 100000)
			for i := range keys {
				keys[i] = Key{ID: fmt.Sprint(i), Name: "Benchmark", Secret: fmt.Sprintf("%s%024d", Prefix, i)}
			}
			if err := Restore(keys); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				b.StopTimer()
				if _, err := Update("rotate-key", Change{Key: keys[0].ID}); err != nil {
					b.Fatal(err)
				}
				if !prepared {
					authPrepared.Store(nil)
				}
				b.StartTimer()
				if _, ok := Authenticate(keys[len(keys)-1].Secret); !ok {
					b.Fatal("unrelated key refused")
				}
			}
		})
	}
}

func TestPreparedAuthenticationOwnsRestrictions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keys := []Key{{ID: "one", Secret: Prefix + "prepared", Models: []string{"vendor/model"}, Limit: &Limit{Tokens: 100}}}
	if err := Restore(keys); err != nil {
		t.Fatal(err)
	}
	keys[0].Models[0] = "*"
	keys[0].Limit.Tokens = 0
	id, ok := Authenticate(keys[0].Secret)
	if !ok || id.Models[0] != "vendor/model" || id.Limit.Tokens != 100 {
		t.Fatal("writer mutated prepared permissions")
	}
}

func TestPreparedAuthenticationAvoidsReparsing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keys := make([]Key, 1000)
	for i := range keys {
		keys[i] = Key{ID: fmt.Sprint(i), Secret: fmt.Sprintf("%s%024d", Prefix, i)}
	}
	if err := Restore(keys); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1, func() {
		authSnapshot.Store(nil)
		if _, ok := Authenticate(keys[999].Secret); !ok {
			t.Fatal("key refused")
		}
	})
	// Reading and hashing the file needs a few buffers; parsing every key
	// allocates thousands. This bound leaves room for buffer growth changes.
	if allocs > 100 {
		t.Fatalf("saved keys were parsed again: %.0f allocations", allocs)
	}
}

func TestPreparedAuthenticationUsesPersistedStrings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	raw := Prefix + "invalid-\xff"
	if err := Restore([]Key{{ID: "one", Secret: raw, Models: []string{"vendor/\xff"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(raw); ok {
		t.Fatal("accepted an unpersisted secret")
	}
	id, ok := Authenticate(Prefix + "invalid-\ufffd")
	if !ok || id.Models[0] != "vendor/\ufffd" {
		t.Fatal("prepared permissions differ from JSON")
	}
}

func TestKeyWriterKeepsWarmAuthenticationUntilReplacement(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldSecret, newSecret := Prefix+"old-secret", Prefix+"new-secret"
	if err := Restore([]Key{{ID: "one", Secret: oldSecret}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate(oldSecret); !ok {
		t.Fatal("key refused")
	}
	old := authSnapshot.Load()
	if old.change.IsZero() {
		t.Skip("platform verifies contents instead of change time")
	}
	target, err := edit.Target(Path())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- edit.Filtered(func(path string, b []byte) []byte {
			if path == target {
				close(entered)
				<-release
			}
			return b
		}, func() error { return Restore([]Key{{ID: "one", Secret: newSecret}}) })
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("writer ended before replacement: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("writer did not reach replacement")
	}
	_, ok := Authenticate(oldSecret)
	reused := authSnapshot.Load() == old
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !ok || !reused {
		t.Fatal("writer invalidated authentication before replacing keys")
	}
	if _, ok := Authenticate(oldSecret); ok {
		t.Fatal("old key accepted after replacement")
	}
	if _, ok := Authenticate(newSecret); !ok {
		t.Fatal("new key refused")
	}
}
