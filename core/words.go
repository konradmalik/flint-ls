package core

import (
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/konradmalik/flint-ls/types"
)

// characters fall into one of these classes and a token is a run of characters
// of a single class, which is how vim decides where a word ends too
type charClass int

const (
	// nothing has been classified yet
	classNone charClass = iota
	classBlank
	classWord
	classPunct
)

// WordEndUtf16 returns the character offset just past the token that starts at
// pos, which is where a range highlighting that token has to end. A token is a
// run of characters of one class, so it ends at the first space after a word, at
// the first letter after an operator, and always at the end of the line.
//
// Only the text from pos onwards is looked at: the caller wants to highlight
// what it points at, so a pos in the middle of a word must not give back a range
// reaching past that word.
//
// pos.Character is returned unchanged when pos points past the end of the line,
// which gives an empty range instead of one highlighting something arbitrary.
//
// lsp can now select encoding from the list that clients send that they support,
// but utf16 is selected if the server does not send it and is required for
// backwards compatibility, so offsets here are utf16 code units.
func WordEndUtf16(text string, pos types.Position) int {
	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) || pos.Character < 0 {
		return pos.Character
	}

	offset := 0
	cls := classNone
	for _, r := range lines[pos.Line] {
		if offset < pos.Character {
			offset += utf16.RuneLen(r)
			continue
		}
		c := classOf(r)
		if cls == classNone {
			cls = c
		} else if c != cls {
			break
		}
		offset += utf16.RuneLen(r)
	}

	// the loop never reaches pos if it points past the last character of the line
	return max(offset, pos.Character)
}

func classOf(r rune) charClass {
	switch {
	case unicode.IsSpace(r):
		return classBlank
	// a mark belongs to the letter it follows, and underscore is punctuation to
	// unicode but part of an identifier in every language a linter runs on
	case r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
		return classWord
	// everything else, operators and brackets included, separates words
	default:
		return classPunct
	}
}
