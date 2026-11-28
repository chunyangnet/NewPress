package content

import (
	"strings"
	"unicode/utf8"
)

// Stream the same frozen normalization across HTML token boundaries. The small
// pending buffer preserves even invalid UTF-8 behavior: removing an inline tag
// can join incomplete byte sequences, just as concatenating text first did.
type publicSearchNormalizer struct {
	out     strings.Builder
	space   bool
	pending [utf8.UTFMax]byte
	n       int
}

func (text *publicSearchNormalizer) writeRune(c rune) {
	if c < utf8.RuneSelf {
		if c == ' ' || c >= '\t' && c <= '\r' {
			text.space = text.out.Len() > 0
			return
		}
		if text.space {
			text.out.WriteByte(' ')
			text.space = false
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		text.out.WriteByte(byte(c))
		return
	}
	if strings.ContainsRune(publicSearchSpaces, c) {
		text.space = text.out.Len() > 0
		return
	}
	if text.space {
		text.out.WriteByte(' ')
		text.space = false
	}
	if lower, ok := publicSearchCase[c]; ok {
		c = lower
	}
	text.out.WriteRune(c)
}

func (text *publicSearchNormalizer) write(data []byte) {
	// Reserve only for actual text tokens, not attributes, comments or ignored
	// elements. There is no full raw-text intermediate allocation.
	text.out.Grow(len(data))
	for text.n > 0 && len(data) > 0 {
		text.pending[text.n] = data[0]
		text.n++
		data = data[1:]
		for text.n > 0 && utf8.FullRune(text.pending[:text.n]) {
			c, size := utf8.DecodeRune(text.pending[:text.n])
			text.writeRune(c)
			text.n = copy(text.pending[:], text.pending[size:text.n])
		}
	}
	if text.n > 0 {
		return
	}
	for len(data) > 0 {
		c, size := utf8.DecodeRune(data)
		if c == utf8.RuneError && size == 1 && !utf8.FullRune(data) {
			text.n = copy(text.pending[:], data)
			return
		}
		text.writeRune(c)
		data = data[size:]
	}
}

func (text *publicSearchNormalizer) finish() string {
	for text.n > 0 {
		c, size := utf8.DecodeRune(text.pending[:text.n])
		text.writeRune(c)
		text.n = copy(text.pending[:], text.pending[size:text.n])
	}
	return text.out.String()
}
