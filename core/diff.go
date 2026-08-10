package core

import (
	"strings"
	"unicode/utf16"

	"github.com/aymanbagabas/go-udiff"
	"github.com/konradmalik/flint-ls/types"
)

// ComputeEdits returns the edits that turn before into after, as whole line
// replacements: every position sits at the start of a line, so no character
// offset has to be translated into the client's position encoding.
func ComputeEdits(before, after string) ([]types.TextEdit, error) {
	edits := udiff.Strings(before, after)
	// the names only go into the "--- / +++" header of the rendered diff, which is
	// not what we are after here
	d, err := udiff.ToUnifiedDiff("", "", before, edits, 0)
	if err != nil {
		return nil, err
	}

	// the index of the last line, which is as far as an edit can reach
	lastLine := strings.Count(before, "\n")

	result := make([]types.TextEdit, 0)
	for _, h := range d.Hunks {
		startLine := h.FromLine - 1
		endLine := startLine
		var newText strings.Builder

		for _, l := range h.Lines {
			// asking for no context lines means a hunk holds nothing but changes,
			// so there are no udiff.Equal lines to copy over
			switch l.Kind {
			case udiff.Delete:
				endLine++
			case udiff.Insert:
				newText.WriteString(l.Content)
			}
		}

		end := types.Position{Line: endLine, Character: 0}
		if endLine > lastLine {
			// the diff counts the end of the file as an implicit newline, so a hunk
			// covering the last line of a document that does not end in one reaches a
			// line the client cannot address. the end of that last line is the same
			// place and is a position that exists.
			end = types.Position{Line: lastLine, Character: utf16Len(before[strings.LastIndex(before, "\n")+1:])}
		}

		result = append(result, types.TextEdit{
			Range: types.Range{
				Start: types.Position{Line: startLine, Character: 0},
				End:   end,
			},
			NewText: newText.String(),
		})
	}

	return result, nil
}

// utf16Len returns the length of s in utf16 code units, which is what lsp
// positions count in.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
