package core

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/konradmalik/flint-ls/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeEdits(t *testing.T) {
	tests := []struct {
		name     string
		before   string
		after    string
		expected []types.TextEdit
	}{
		{
			name:     "no changes",
			before:   "line1\nline2\nline3\n",
			after:    "line1\nline2\nline3\n",
			expected: []types.TextEdit{},
		},
		{
			name:   "single line insertion at beginning",
			before: "line2\nline3\n",
			after:  "line1\nline2\nline3\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 0, Character: 0},
					},
					NewText: "line1\n",
				},
			},
		},
		{
			name:   "single line insertion at end",
			before: "line1\nline2\n",
			after:  "line1\nline2\nline3\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 2, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
					NewText: "line3\n",
				},
			},
		},
		{
			name:   "single line insertion in middle",
			before: "line1\nline3\n",
			after:  "line1\nline2\nline3\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 1, Character: 0},
					},
					NewText: "line1\nline2\n",
				},
			},
		},
		{
			name:   "multiple line insertion",
			before: "line1\nline4\n",
			after:  "line1\nline2\nline3\nline4\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 1, Character: 0},
					},
					NewText: "line1\nline2\nline3\n",
				},
			},
		},
		{
			name:   "single line deletion at beginning",
			before: "line1\nline2\nline3\n",
			after:  "line2\nline3\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 1, Character: 0},
					},
				},
			},
		},
		{
			name:   "single line deletion at end",
			before: "line1\nline2\nline3\n",
			after:  "line1\nline2\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 2, Character: 0},
						End:   types.Position{Line: 3, Character: 0},
					},
				},
			},
		},
		{
			name:   "single line deletion in middle",
			before: "line1\nline2\nline3\n",
			after:  "line1\nline3\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
					NewText: "line1\n",
				},
			},
		},
		{
			name:   "multiple line deletion",
			before: "line1\nline2\nline3\nline4\n",
			after:  "line1\nline4\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 3, Character: 0},
					},
					NewText: "line1\n",
				},
			},
		},
		{
			name:   "line replacement",
			before: "line1\nold_line\nline3\n",
			after:  "line1\nnew_line\nline3\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
					NewText: "new_line\n",
				},
			},
		},
		{
			name:   "multiple changes",
			before: "line1\nline2\nline5\n",
			after:  "line1\nline3\nline4\nline5\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
					NewText: "line3\nline4\n",
				},
			},
		},
		{
			name:     "empty to empty",
			before:   "",
			after:    "",
			expected: []types.TextEdit{},
		},
		{
			name:   "empty to content",
			before: "",
			after:  "line1\nline2\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 0, Character: 0},
					},
					NewText: "line1\nline2\n",
				},
			},
		},
		{
			name:   "content to empty",
			before: "line1\nline2\n",
			after:  "",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
				},
			},
		},
		{
			// there is no line 2 to point at, so the edit has to end at the end of
			// line 1 instead
			name:   "no trailing newline in before",
			before: "line1\nline2",
			after:  "line1\nline3",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 1, Character: 5},
					},
					NewText: "line3",
				},
			},
		},
		{
			name:   "single line without newline",
			before: "a",
			after:  "b",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 0, Character: 0},
						End:   types.Position{Line: 0, Character: 1},
					},
					NewText: "b",
				},
			},
		},
		{
			// the clamped end counts utf16 code units, not bytes: this line is 5
			// characters but 6 bytes long
			name:   "multibyte last line without newline",
			before: "x\nhéllo",
			after:  "x\nhello",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 1, Character: 5},
					},
					NewText: "hello",
				},
			},
		},
		{
			// and an astral character is two of those code units
			name:   "astral last line without newline",
			before: "x\n😊",
			after:  "x\n😊😊",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 1, Character: 2},
					},
					NewText: "😊😊",
				},
			},
		},
		{
			name:   "trailing newline added",
			before: "line1\nline2",
			after:  "line1\nline2\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 1, Character: 5},
					},
					NewText: "line2\n",
				},
			},
		},
		{
			name:   "no trailing newline in after",
			before: "line1\nline2\n",
			after:  "line1\nline3",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
					NewText: "line3",
				},
			},
		},
		{
			name:   "single character line",
			before: "a\nb\nc\n",
			after:  "a\nx\nc\n",
			expected: []types.TextEdit{
				{
					Range: types.Range{
						Start: types.Position{Line: 1, Character: 0},
						End:   types.Position{Line: 2, Character: 0},
					},
					NewText: "x\n",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := ComputeEdits(tt.before, tt.after)
			assert.NoError(t, err)

			assert.Equal(t, tt.expected, actual)

			// applyEdits rejects positions the document does not have and edits that
			// overlap or run backwards, so this covers the ranges as well as the text
			assert.Equal(t, tt.after, applyEdits(t, tt.before, actual))
		})
	}
}

func TestComputeEditsLargeInput(t *testing.T) {
	var before strings.Builder
	var after strings.Builder

	// Create 1000 lines
	for i := range 1000 {
		if i%2 == 0 {
			before.WriteString("line" + string(rune('0'+i%10)) + "\n")
			after.WriteString("line" + string(rune('0'+i%10)) + "\n")
		} else {
			before.WriteString("old" + string(rune('0'+i%10)) + "\n")
			after.WriteString("new" + string(rune('0'+i%10)) + "\n")
		}
	}

	edits, err := ComputeEdits(before.String(), after.String())
	assert.NoError(t, err)

	assert.Equal(t, after.String(), applyEdits(t, before.String(), edits))
}

func TestComputeEditsComplexScenario(t *testing.T) {
	before := `package main

import "fmt"

func main() {
    fmt.Println("Hello, World!")
    x := 42
    fmt.Println(x)
}
`

	after := `package main

import (
    "fmt"
    "os"
)

func main() {
    fmt.Println("Hello, Go!")
    y := 100
    fmt.Println(y)
    os.Exit(0)
}
`

	edits, err := ComputeEdits(before, after)
	assert.NoError(t, err)

	assert.Equal(t, after, applyEdits(t, before, edits))
}

// applyEdits applies LSP text edits the way a client does, and fails the test on
// anything a client could not apply: a position the document does not have, or
// edits that overlap or arrive out of order.
func applyEdits(t *testing.T, text string, edits []types.TextEdit) string {
	t.Helper()

	var result strings.Builder
	offset := 0
	for i, e := range edits {
		start := offsetOf(t, text, e.Range.Start)
		end := offsetOf(t, text, e.Range.End)
		require.LessOrEqual(t, start, end, "edit %d ends before it starts: %+v", i, e.Range)
		require.LessOrEqual(t, offset, start, "edit %d overlaps the one before it: %+v", i, e.Range)

		result.WriteString(text[offset:start])
		result.WriteString(e.NewText)
		offset = end
	}
	result.WriteString(text[offset:])

	return result.String()
}

// offsetOf converts an LSP position into a byte offset, failing the test if the
// document has no such position. Characters count utf16 code units.
func offsetOf(t *testing.T, text string, pos types.Position) int {
	t.Helper()

	lines := strings.Split(text, "\n")
	require.GreaterOrEqual(t, pos.Line, 0, "negative line in %+v", pos)
	require.Less(t, pos.Line, len(lines), "no line %d in %q", pos.Line, text)

	offset := 0
	for _, l := range lines[:pos.Line] {
		offset += len(l) + 1 // the newline that the split dropped
	}

	character := 0
	for i, r := range lines[pos.Line] {
		if character == pos.Character {
			return offset + i
		}
		character += utf16.RuneLen(r)
	}
	// the position may sit just past the last character of the line, but no further,
	// and it may not sit inside a surrogate pair
	require.Equal(t, pos.Character, character, "no character %d on line %d of %q", pos.Character, pos.Line, text)

	return offset + len(lines[pos.Line])
}
