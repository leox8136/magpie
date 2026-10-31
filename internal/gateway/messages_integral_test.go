package gateway

import (
	"bytes"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestWholeMessagesNumbersPreserveAndReleaseText(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"8.0"},{"input":{"n":8.0,"big":9007199254740993,"s":"8.0","nested":[2.50,8e0]},"type":"tool_use"},{"type":"thinking","thinking":"x"}]}`)
	want := bytes.Replace(body, []byte(`"n":8.0`), []byte(`"n":8`), 1)
	for _, size := range []int{1, 7, len(body)} {
		tidy := &intTidy{proto: provider.Anthropic}
		var got []byte
		for i := 0; i < len(body); i += size {
			got = append(got, tidy.write(body[i:min(i+size, len(body))])...)
		}
		got = append(got, tidy.flush()...)
		if !bytes.Equal(got, want) {
			t.Fatalf("chunk %d changed unrelated JSON: %s", size, got)
		}
	}
	tidy := &intTidy{proto: provider.Anthropic}
	prefix := []byte(`{"content":[{"type":"text","text":"`)
	if got := tidy.write(prefix); !bytes.Equal(got, prefix) {
		t.Fatal("text held until response ends")
	}
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	for i := 0; i < 512; i++ {
		if got := tidy.write(chunk); !bytes.Equal(got, chunk) {
			t.Fatal("large text held")
		}
	}
	tail := []byte(`"},{"type":"tool_use","input":{"n":8.`)
	if got := append(tidy.write(tail), tidy.flush()...); !bytes.Equal(got, tail) {
		t.Fatal("incomplete input changed")
	}
}
