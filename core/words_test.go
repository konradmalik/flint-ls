package core

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/konradmalik/flint-ls/types"
	"github.com/stretchr/testify/assert"
)

func TestWordEndUtf16(t *testing.T) {
	tests := []struct {
		name string
		text string
		// positions here are LSP, so lines and chars are 0 indexed
		pos types.Position
		// the text a client would highlight, so from pos up to the returned end
		expected string
	}{
		{"start of ascii word", "hello world", types.Position{Line: 0, Character: 0}, "hello"},
		{"middle of ascii word", "hello world", types.Position{Line: 0, Character: 1}, "ello"},
		{"last char of word", "hello world", types.Position{Line: 0, Character: 4}, "o"},
		{"start of second word", "hello world", types.Position{Line: 0, Character: 6}, "world"},
		{"between words", "hello world", types.Position{Line: 0, Character: 5}, " "},
		{"tab between words", "foo\tbar", types.Position{Line: 0, Character: 3}, "\t"},
		{"punctuation separated", "foo,bar", types.Position{Line: 0, Character: 0}, "foo"},
		{"punctuation separated second", "foo,bar", types.Position{Line: 0, Character: 4}, "bar"},
		{"underscores kept", "foo_bar baz", types.Position{Line: 0, Character: 0}, "foo_bar"},
		{"digits kept", "arg1 arg2", types.Position{Line: 0, Character: 0}, "arg1"},
		{"hyphen separated", "foo-bar baz", types.Position{Line: 0, Character: 4}, "bar"},
		{"only punctuation", "!!!", types.Position{Line: 0, Character: 0}, "!!!"},
		{"method call", "someobject.somemethod(arg1,arg2)", types.Position{Line: 0, Character: 11}, "somemethod"},
		{"function def", "func(arg1 string, arg2 int)", types.Position{Line: 0, Character: 0}, "func"},
		{"function def arg", "func(arg1 string, arg2 int)", types.Position{Line: 0, Character: 5}, "arg1"},

		// operators are symbols, not unicode punctuation, but still separate words
		{"assignment", "foo=bar", types.Position{Line: 0, Character: 0}, "foo"},
		{"assignment operator", "foo=bar", types.Position{Line: 0, Character: 3}, "="},
		{"arrow", "x->y", types.Position{Line: 0, Character: 1}, "->"},
		{"plus", "foo+bar", types.Position{Line: 0, Character: 0}, "foo"},
		{"shell variable sigil", "$PATH", types.Position{Line: 0, Character: 0}, "$"},
		{"shell variable name", "$PATH", types.Position{Line: 0, Character: 1}, "PATH"},

		// non ascii
		{"unicode accents", "mañana café", types.Position{Line: 0, Character: 0}, "mañana"},
		{"combining marks", "éclair x", types.Position{Line: 0, Character: 0}, "éclair"},
		{"precomposed accent", "éclair x", types.Position{Line: 0, Character: 0}, "éclair"},
		{"CJK characters", "你好 世界", types.Position{Line: 0, Character: 0}, "你好"},
		{"CJK punctuation separates", "你好。世界", types.Position{Line: 0, Character: 0}, "你好"},
		{"emoji", "hello 😊 world", types.Position{Line: 0, Character: 6}, "😊"},
		// the emoji is a surrogate pair, so its length must not be counted twice
		{"word before emoji", "a😊", types.Position{Line: 0, Character: 0}, "a"},
		{"astral letter is a word", "A\U0001D400b", types.Position{Line: 0, Character: 0}, "A\U0001D400b"},
		{"after astral letter", "A\U0001D400,b", types.Position{Line: 0, Character: 3}, ","},

		// out of range positions give an empty range
		{"position past end", "hello", types.Position{Line: 0, Character: 10}, ""},
		{"position at end of line", "hello", types.Position{Line: 0, Character: 5}, ""},
		{"empty string", "", types.Position{Line: 0, Character: 0}, ""},
		{"negative character", "hello", types.Position{Line: 0, Character: -1}, ""},
		{"line past end", "hello", types.Position{Line: 3, Character: 0}, ""},
		{"negative line", "hello", types.Position{Line: -1, Character: 0}, ""},

		// multi-line tests
		{"start of second line", "hello\nworld", types.Position{Line: 1, Character: 0}, "world"},
		{"middle of second line", "hello\nworld", types.Position{Line: 1, Character: 3}, "ld"},
		{"second word on second line", "hello\nworld test", types.Position{Line: 1, Character: 6}, "test"},
		{"token stops at end of line", "hello\nworld", types.Position{Line: 0, Character: 0}, "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, highlighted(tt.text, tt.pos))
		})
	}
}

// highlighted is the text a client would highlight for a range that starts at
// pos and ends where WordEndUtf16 says, clamped to the line the way a client
// clamps a range it cannot resolve
func highlighted(text string, pos types.Position) string {
	end := WordEndUtf16(text, pos)
	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) {
		return ""
	}
	chars := utf16.Encode([]rune(lines[pos.Line]))
	start := min(max(pos.Character, 0), len(chars))
	return string(utf16.Decode(chars[start:min(max(end, start), len(chars))]))
}
