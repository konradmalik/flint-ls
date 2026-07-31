package core

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konradmalik/flint-ls/types"
)

func TestNewHandlerWithoutLanguages(t *testing.T) {
	h := NewHandler(&types.Config{})

	_, err := h.snapshot("file:///nope")

	assert.Error(t, err, "a handler without languages should be usable, just useless")
}

func TestEnsureUnchanged(t *testing.T) {
	uri := types.DocumentURI("file:///a.txt")
	h := &LangHandler{files: map[types.DocumentURI]*fileRef{uri: {Version: 7}}}

	assert.NoError(t, h.ensureUnchanged(uri, 7))

	assert.ErrorIs(t, h.ensureUnchanged(uri, 6), ErrDocumentChanged,
		"a different version means the result is stale")

	// closed and edited are the same answer to the caller: the result is unusable
	assert.ErrorIs(t, h.ensureUnchanged("file:///gone.txt", 7), ErrDocumentChanged,
		"a document that went away cannot be the one that was processed")
}

// TestConcurrentDocumentSyncWhileLinting covers the overlap the server lives
// with: document sync notifications arrive on the connection's read loop while
// lint and format runs execute on their own goroutines. Run under -race.
func TestConcurrentDocumentSyncWhileLinting(t *testing.T) {
	languages := map[string][]types.Language{
		"test": {{
			LintCommand:        "echo 1:problem",
			LintFormats:        []string{"%l:%m"},
			LintStdin:          true,
			LintIgnoreExitCode: true,
		}},
	}
	h := NewHandler(&types.Config{Languages: &languages})

	uris := make([]types.DocumentURI, 4)
	dir := t.TempDir()
	for i := range uris {
		uris[i] = ParseLocalFileToURI(filepath.Join(dir, string(rune('a'+i))+".txt"))
		require.NoError(t, h.OpenFile(uris[i], "test", 1, "some text\n"))
	}

	const rounds = 20
	var wg sync.WaitGroup

	for _, uri := range uris {
		wg.Go(func() {
			for i := range rounds {
				// the document may be closed right now by the goroutine below;
				// that is an error, not a crash
				_ = h.UpdateFile(uri, "changed text\n", &i)
			}
		})

		wg.Go(func() {
			for range rounds {
				// a document may have been closed by the goroutine below, and
				// linting a document that is gone is an error, not a crash
				_ = h.RunAllLinters(t.Context(), &recordingReporter{}, uri, types.EventTypeChange)
			}
		})

		wg.Go(func() {
			for range rounds {
				require.NoError(t, h.CloseFile(uri))
				require.NoError(t, h.OpenFile(uri, "test", 1, "reopened\n"))
			}
		})
	}

	wg.Go(func() {
		for range rounds {
			h.UpdateConfiguration(&types.Config{Languages: &languages})
		}
	})

	wg.Wait()
}
