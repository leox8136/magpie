package budget

import (
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/usage"
	"testing"
	"time"
)

// The issuer's warm and reloaded windows must count a child's settlement once.
func TestIssuedSettlementKeepsParentBudget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	Forget()
	t.Cleanup(Forget)
	now := time.Now()
	parent := access.Identity{KeyID: "parent", KeyName: "parent", Limit: &access.Limit{Period: "day", Tokens: 10}}
	child := access.Identity{KeyID: "child", KeyName: "child", Issuer: &parent}
	release, refused := Reserve(child, 1, "", now)
	if refused != nil {
		t.Fatal(refused)
	}
	Append(usage.Record{Time: now, CallerKeyID: "child", CallerIssuerID: "parent", Input: 10, Status: 200})
	release()
	for _, reload := range []bool{false, true} {
		if reload {
			Forget()
		}
		release, refused = Reserve(child, 1, "", now)
		if refused == nil {
			release()
			t.Fatalf("reload=%v: exhausted issuer admitted child", reload)
		}
		all := All(access.Key{ID: "parent", Limit: parent.Limit}, now)
		if len(all) != 1 || all[0].Error != "" {
			t.Fatalf("reload=%v: %+v", reload, all)
		}
	}
}
