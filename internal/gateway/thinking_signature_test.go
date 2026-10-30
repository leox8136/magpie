package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Grok Build reads a Messages thinking block as ContentBlock::Thinking
// {thinking, signature}, both required (its grok 1.0.67 binary), and
// turned a reply from minimax-code-global/MiniMax-M3.1-Flash away with
// "serialization error: missing field `signature`" (uk.jent on Discord).
//
// No bytes of MiniMax's reply were captured: there is no MiniMax Code
// account here. The unsigned shapes below are what MiniMax's Anthropic API
// docs read (`block.type == "thinking"`, `block.thinking`, thinking_delta;
// no signature or signature_delta named anywhere), and what a translated
// reply (Chat's reasoning_content) gave before.

// minimaxUnsignedStream is a Messages stream whose thinking block starts
// with no signature and gets none before its stop.
var minimaxUnsignedStream = sse(
	`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"msg_mm1","type":"message","role":"assistant","model":"MiniMax-M3.1-Flash","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":0}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The user says hi. <greet back>"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello!"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":1}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
)

const minimaxUnsignedWhole = `{"id":"msg_mm2","type":"message","role":"assistant","model":"MiniMax-M3.1-Flash",` +
	`"content":[{"type":"thinking","thinking":"The user says hi. <greet back>"},{"type":"text","text":"Hello!"}],` +
	`"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":9}}`

// thinkingBlocks are the thinking blocks a Messages reply gives its
// client: each stream's content_block_start, or a whole reply's content.
func thinkingBlocks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var blocks []map[string]any
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		var reply struct {
			Content []map[string]any `json:"content"`
		}
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatalf("reply isn't JSON: %v\n%s", err, body)
		}
		for _, b := range reply.Content {
			if b["type"] == "thinking" {
				blocks = append(blocks, b)
			}
		}
		return blocks
	}
	for _, ev := range events(body) {
		if ev["type"] != "content_block_start" {
			continue
		}
		if b, _ := ev["content_block"].(map[string]any); b["type"] == "thinking" {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// TestMessagesThinkingIsSigned: every thinking block magpie gives a
// Messages client says a signature, a string, whether the reply was
// relayed from a vendor's Messages endpoint that wrote none (MiniMax's) or
// translated from Chat's reasoning_content, streamed or whole. The
// thinking itself is unchanged, and what magpie adds is empty, never a
// made-up signature.
func TestMessagesThinkingIsSigned(t *testing.T) {
	for _, tt := range []struct {
		name   string
		proto  provider.Protocol
		stream bool
		f      *fake
	}{
		{"relayed stream", provider.Anthropic, true, &fake{reply: minimaxUnsignedStream}},
		{"relayed whole", provider.Anthropic, false, &fake{reply: minimaxUnsignedWhole, ctype: "application/json"}},
		{"translated stream", provider.Chat, true, &fake{reply: sse(glmChatThinksSlowly...)}},
		// the upstream is asked streamed; the client gets one body
		{"translated whole", provider.Chat, false, &fake{reply: sse(glmChatThinksSlowly...)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup(t, tt.proto, tt.f)
			stream := "false"
			if tt.stream {
				stream = "true"
			}
			code, body := post(t, "/v1/messages", `{"model":"fake/m1","max_tokens":64,"stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			blocks := thinkingBlocks(t, body)
			if len(blocks) != 1 {
				t.Fatalf("thinking blocks = %d, want 1:\n%s", len(blocks), body)
			}
			sig, ok := blocks[0]["signature"].(string)
			if !ok {
				t.Fatalf("thinking block has no signature string, as Grok Build turns away: %v\n%s", blocks[0], body)
			}
			if sig != "" {
				t.Errorf("signature = %q, want the empty one magpie adds", sig)
			}
			if _, ok := blocks[0]["thinking"].(string); !ok {
				t.Errorf("thinking block has no thinking string: %v", blocks[0])
			}
			if tt.proto == provider.Anthropic && !strings.Contains(body, "<greet back>") && !strings.Contains(body, `\u003cgreet back\u003e`) {
				t.Errorf("the thinking's text was lost:\n%s", body)
			}
		})
	}
}

// TestSignedThinkingRelayedAsItCame: a reply whose thinking is signed
// already (Anthropic's own) goes to the client byte for byte, its
// signature as Anthropic wrote it.
func TestSignedThinkingRelayedAsItCame(t *testing.T) {
	signed := strings.NewReplacer(
		`"content_block":{"type":"thinking","thinking":""}`, `"content_block":{"type":"thinking","thinking":"","signature":""}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCkYIBxgCKkDreal"}}`+"\n\n"+
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	).Replace(minimaxUnsignedStream)
	whole := strings.Replace(minimaxUnsignedWhole, `"thinking":"The user says hi. <greet back>"}`, `"thinking":"The user says hi. <greet back>","signature":"EqQBCkYIBxgCKkDreal"}`, 1)
	for _, tt := range []struct {
		name, reply, ctype, stream string
	}{
		{"stream", signed, "", "true"},
		{"whole", whole, "application/json", "false"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup(t, provider.Anthropic, &fake{reply: tt.reply, ctype: tt.ctype})
			code, body := post(t, "/v1/messages", `{"model":"fake/m1","max_tokens":64,"stream":`+tt.stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			if body != tt.reply {
				t.Errorf("signed reply changed on its way:\n got %s\nwant %s", body, tt.reply)
			}
		})
	}
}

// TestSigFieldTidyCutAnywhere: a stream cut at any byte gives the same
// lines as one read whole, and a thinking block's text that mentions
// "thinking" in a delta is left alone.
func TestSigFieldTidyCutAnywhere(t *testing.T) {
	whole := (&sigFieldTidy{sse: true}).write([]byte(minimaxUnsignedStream))
	for cut := 1; cut < len(minimaxUnsignedStream); cut += 7 {
		tidy := &sigFieldTidy{sse: true}
		got := append(tidy.write([]byte(minimaxUnsignedStream[:cut])), tidy.write([]byte(minimaxUnsignedStream[cut:]))...)
		got = append(got, tidy.flush()...)
		if string(got) != string(whole) {
			t.Fatalf("cut at %d:\n got %s\nwant %s", cut, got, whole)
		}
	}
	if !strings.Contains(string(whole), `"content_block":{"signature":"","thinking":"","type":"thinking"}`) {
		t.Errorf("thinking start not signed:\n%s", whole)
	}
	if !strings.Contains(string(whole), `"thinking_delta","thinking":"The user says hi. <greet back>"}`) {
		t.Errorf("a delta changed:\n%s", whole)
	}
	// a null signature is none too
	if b, ok := unsignedThinking(json.RawMessage(`{"type":"thinking","thinking":"x","signature":null}`)); !ok || string(b) != `{"signature":"","thinking":"x","type":"thinking"}` {
		t.Errorf("null signature: %s, %v", b, ok)
	}
	// redacted_thinking keeps its data, untouched
	if _, ok := unsignedThinking(json.RawMessage(`{"type":"redacted_thinking","data":"abc"}`)); ok {
		t.Error("redacted_thinking was changed")
	}
}

func TestSigFieldTidyWholePreservesOtherBlocks(t *testing.T) {
	// Two changed blocks around a signed block protect splice offsets; tool
	// data must retain literal bytes and precision, even beside thinking.
	tool := `{"type":"tool_use", "input":{"n":9007199254740993,"html":"<a>&"},"id":"t","name":"f"}`
	signed := `{"type":"thinking", "thinking":"signed", "signature":"real"}`
	body := []byte(`{"content":[{"type":"thinking","thinking":"one"},` + tool + `,` + signed + `,{"type":"thinking","thinking":"two","signature":null}]}`)
	tidy := &sigFieldTidy{}
	out := append(tidy.write(body), tidy.flush()...)
	if !json.Valid(out) || !bytes.Contains(out, []byte(tool)) || !bytes.Contains(out, []byte(signed)) {
		t.Fatalf("unrelated payload changed: %s", out)
	}
	for _, block := range thinkingBlocks(t, string(out)) {
		if _, ok := block["signature"].(string); !ok {
			t.Fatalf("signature missing: %v", block)
		}
	}
	// EOF inside a final SSE line is flushed unchanged when it cannot be
	// parsed, so the gateway's existing cut/error handling still sees it.
	tail := []byte("data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"thinking\"")
	stream := &sigFieldTidy{sse: true}
	if len(stream.write(tail)) != 0 || !bytes.Equal(stream.flush(), tail) {
		t.Fatal("partial tail lost")
	}
}

func TestWholeSignaturesChunks(t *testing.T) {
	cases := []string{
		minimaxUnsignedWhole,
		`{"meta":{"content":[{"type":"thinking"}]},"content":[{"thinking":"escaped \\\" } , text","signature":null,"type":"thinking"},{"type":"tool_use","input":{"type":"thinking","content":[1,2],"n":9007199254740993}},{"signature":"real","type":"thinking","thinking":"signed"}]}`,
		`{"cont\u0065nt":[{"type":"thinking","thinking":"x"},{"type":"text","text":"y"}],"usage":{"output_tokens":4}}`,
	}
	for _, body := range cases {
		want := []byte(body)
		if fixed, ok := signedContent(want); ok {
			want = fixed
		}
		for _, chunk := range []int{1, 7, 32768} {
			tidy := &sigFieldTidy{}
			var got []byte
			for i := 0; i < len(body); i += chunk {
				got = append(got, tidy.write([]byte(body[i:min(i+chunk, len(body))]))...)
			}
			got = append(got, tidy.flush()...)
			var a, b any
			if json.Unmarshal(got, &a) != nil || json.Unmarshal(want, &b) != nil {
				t.Fatalf("invalid JSON: %s", got)
			}
			aa, _ := json.Marshal(a)
			bb, _ := json.Marshal(b)
			if !bytes.Equal(aa, bb) {
				t.Fatalf("chunk %d: %s != %s", chunk, got, want)
			}
		}
	}
}

func TestWholeSignaturesReleasesLargeText(t *testing.T) {
	tidy := &sigFieldTidy{}
	prefix := []byte(`{"content":[{"type":"text","text":"`)
	if got := tidy.write(prefix); !bytes.Equal(got, prefix) {
		t.Fatalf("text withheld: %s", got)
	}
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	for i := 0; i < 512; i++ {
		if got := tidy.write(chunk); !bytes.Equal(got, chunk) {
			t.Fatal("large text withheld")
		}
		if cap(tidy.whole.block) > 1024 {
			t.Fatal("large text retained")
		}
	}
	tail := []byte(`"},{"type":"thinking","thinking":"unfinished`)
	got := append(tidy.write(tail), tidy.flush()...)
	if !bytes.Equal(got, tail) {
		t.Fatalf("truncated block changed: %s", got)
	}
}

func BenchmarkWholeSignatureResponse(b *testing.B) {
	for _, kind := range []string{"text", "tool", "thinking"} {
		payload := strings.Repeat("x", 16<<20)
		block := `{"type":"text","text":"` + payload + `"}`
		if kind == "tool" {
			block = `{"type":"tool_use","input":{"payload":"` + payload + `"}}`
		}
		if kind == "thinking" {
			block = `{"type":"thinking","thinking":"` + payload + `"}`
		}
		body := []byte(`{"content":[` + block + `],"usage":{"output_tokens":1}}`)
		for _, old := range []bool{true, false} {
			name := kind + "/incremental"
			if old {
				name = kind + "/whole-buffer"
			}
			b.Run(name, func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					var buf []byte
					tidy := &sigFieldTidy{}
					for i := 0; i < len(body); i += 32 << 10 {
						chunk := body[i:min(i+32<<10, len(body))]
						if old {
							buf = append(buf, chunk...)
						} else {
							tidy.write(chunk)
						}
					}
					if old {
						signedContent(buf)
					} else {
						tidy.flush()
					}
				}
			})
		}
	}
}

// Pre-change whole-body implementation, retained only as the benchmark and
// semantic reference for the incremental implementation.
// signedContent is a whole Messages reply whose thinking blocks without a
// signature are given an empty one, and whether it had such a block.
func signedContent(b []byte) ([]byte, bool) {
	if !bytes.Contains(b, thinkingType) {
		return nil, false
	}
	content := gjson.GetBytes(b, "content")
	if !content.IsArray() {
		return nil, false
	}
	// Splice only changed blocks. Re-encoding the entire reply would copy
	// large text/tool payloads repeatedly and alter unrelated wire bytes.
	var out []byte
	last := 0
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").Str != "thinking" || block.Get("signature").Type == gjson.String {
			return true
		}
		raw := b[block.Index : block.Index+len(block.Raw)]
		fixed, ok := unsignedThinking(raw)
		if !ok {
			return true
		}
		if out == nil {
			out = make([]byte, 0, len(b)+len(fixed)-len(raw))
		}
		out = append(out, b[last:block.Index]...)
		out = append(out, fixed...)
		last = block.Index + len(block.Raw)
		return true
	})
	if out == nil {
		return nil, false
	}
	return append(out, b[last:]...), true
}
