package gateway

import (
	"github.com/yetone/magpie/internal/access"
	"net/http"
	"testing"
)

// Every turn under a mounted WebSocket must reauthenticate the same key.
func TestMountedSocketRechecksRevocation(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_BASE_PATH", "/magpie")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	keys, secrets := newCaller(t, "socket")
	u := &respUp{}
	sockUp(t, u)
	_, ts := sockServer(t)
	c, _, err := dialSock(t, ts, "/magpie/v1/responses", http.Header{"Authorization": {"Bearer " + secrets[0]}, "X-Test-Remote": {"192.0.2.1:4567"}})
	if err != nil {
		t.Fatal(err)
	}
	got := c.turn(create("", userHi))
	if last(got).Get("type").String() != "response.completed" {
		t.Fatalf("mounted turn: %v", got)
	}
	if _, err := access.Update("remove-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	got = c.turn(create(last(got).Get("response.id").String(), userMore))
	if last(got).Get("type").String() == "response.completed" || u.calls() != 1 {
		t.Fatalf("revoked socket reached vendor: %v calls=%d", got, u.calls())
	}
}
