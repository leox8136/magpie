package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A child still holds its parent's raw input when the parent is evicted.
func TestPreviousResponseChargesInheritedHistory(t *testing.T) {
	up := newPrevUp(t, func(n int, _ []byte) (int, string, string) {
		return 200, "text/event-stream", strings.ReplaceAll(chatSSEReply(fmt.Sprintf("answer %d", n)), `"c1"`, fmt.Sprintf(`"c%d"`, n))
	})
	s := prevSandbox(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.srv.URL + "/v1"})
	prevResponses.cap = 4096
	input := strings.Repeat("x", 1000)
	first := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","input":"`+input+`"}`))
	second := responseID(t, askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+first+`","input":"`+input+`"}`))
	res := askResponses(t, s, "", `{"model":"fake/m1","previous_response_id":"`+second+`","input":"more"}`)
	if res.Code != 400 || !strings.Contains(res.Body.String(), "previous_response_not_found") {
		t.Fatalf("oversized inherited history stayed cached: %d %s", res.Code, res.Body.String())
	}
	if up.calls() != 2 {
		t.Fatalf("evicted history reached upstream: %d", up.calls())
	}
}
