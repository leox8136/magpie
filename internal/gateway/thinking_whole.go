package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

// wholeContent repairs selected Messages content blocks. Ordinary text passes
// through once its type is known; only an undecided block or one needing
// repair is retained, preserving field order and unrelated JSON bytes.
type wholeContent struct {
	signatures     bool
	numbers        bool
	depth          int
	quoted, escape bool
	key            []byte
	content        bool
	array          int
	block          []byte
	active, direct bool
}

func (s *wholeContent) write(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		// Text and tool arguments dominate large replies. Copy ordinary
		// string spans in bulk, retaining only JSON quote/escape state.
		if s.quoted && !s.escape && (s.depth != 1 || len(s.key) >= 64) {
			n := bytes.IndexAny(b[i:], "\\\"")
			if n < 0 {
				n = len(b) - i
			}
			if n > 0 {
				if s.active && !s.direct {
					s.block = append(s.block, b[i:i+n]...)
				} else {
					out = append(out, b[i:i+n]...)
				}
				i += n - 1
				continue
			}
		}
		c := b[i]
		if !s.quoted && c == '{' && s.array != 0 && s.depth == s.array {
			s.active, s.direct = true, false
			s.block = s.block[:0]
		}
		if s.active && !s.direct {
			s.block = append(s.block, c)
		} else {
			out = append(out, c)
		}
		if s.quoted {
			if s.depth == 1 && len(s.key) < 64 {
				s.key = append(s.key, c)
			}
			if s.escape {
				s.escape = false
				continue
			}
			if c == '\\' {
				s.escape = true
			} else if c == '"' {
				s.quoted = false
			}
			continue
		}
		switch c {
		case '"':
			s.quoted = true
			if s.depth == 1 {
				s.key = append(s.key[:0], c)
			}
		case ':':
			if s.depth == 1 {
				var key string
				s.content = json.Unmarshal(s.key, &key) == nil && key == "content"
			}
		case '[':
			if s.depth == 1 && s.content {
				s.array = 2
			}
			s.depth++
		case '{':
			s.depth++
		case ',':
			if s.active && !s.direct && s.depth == s.array+1 {
				typ := gjson.GetBytes(s.block, "type")
				sig := gjson.GetBytes(s.block, "signature")
				pass := !(s.signatures && typ.Str == "thinking" && sig.Type != gjson.String) && !(s.numbers && typ.Str == "tool_use")
				if typ.Type == gjson.String && pass {
					out = append(out, s.block...)
					s.block = nil
					s.direct = true
				}
			}
		case '}':
			if s.active && s.depth == s.array+1 {
				if !s.direct {
					var fixed []byte
					var ok bool
					if s.numbers && gjson.GetBytes(s.block, "type").Str == "tool_use" {
						fixed, ok = integralToolInput(s.block)
					} else if s.signatures {
						fixed, ok = unsignedThinking(s.block)
					}
					if ok {
						out = append(out, fixed...)
					} else {
						out = append(out, s.block...)
					}
					s.block = nil
				}
				s.active = false
			}
			s.depth--
		case ']':
			if s.depth == s.array {
				s.array = 0
			}
			s.depth--
		}
	}
	return out
}

func (s *wholeContent) flush() []byte {
	b := s.block
	s.block = nil
	return b // an incomplete block stays incomplete; never fabricate a reply
}

// Only a tool's input changes; surrounding JSON bytes and other blocks keep
// their spelling, including large integers and escaped strings.
func integralToolInput(block []byte) ([]byte, bool) {
	if gjson.GetBytes(block, "type").Str != "tool_use" {
		return nil, false
	}
	input := gjson.GetBytes(block, "input")
	if !input.Exists() || input.Index <= 0 {
		return nil, false
	}
	fixed := wholeArgs(input.Raw)
	if fixed == input.Raw {
		return nil, false
	}
	out := make([]byte, 0, len(block)-len(input.Raw)+len(fixed))
	out = append(out, block[:input.Index]...)
	out = append(out, fixed...)
	out = append(out, block[input.Index+len(input.Raw):]...)
	return out, true
}
