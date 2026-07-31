package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konradmalik/flint-ls/types"
)

func TestNewHandlerWithoutLanguages(t *testing.T) {
	h := NewHandler(nil)

	_, err := h.snapshot("file:///nope")

	assert.Error(t, err, "a handler without languages should be usable, just useless")
}

// TestReplaceMagicStrings pins down the one rule that keeps existing configs
// working: a placeholder the config already quoted is substituted exactly as it
// was before, and only a bare one gains quotes.
func TestReplaceMagicStrings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the expectations below spell out POSIX shell quoting; cmd quotes differently")
	}

	const (
		fname = "/home/u/my dir/a file (1).ts"
		root  = "/home/u/my dir"
	)

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "bare placeholder is quoted",
			command: "eslint --stdin --stdin-filename ${INPUT}",
			want:    `eslint --stdin --stdin-filename '/home/u/my dir/a file (1).ts'`,
		},
		{
			name:    "bare placeholder after an equals sign is quoted",
			command: "eslint --stdin-filename=${INPUT}",
			want:    `eslint --stdin-filename='/home/u/my dir/a file (1).ts'`,
		},
		{
			name:    "placeholder the config quoted is left alone",
			command: `cspell lint "${INPUT}"`,
			want:    `cspell lint "/home/u/my dir/a file (1).ts"`,
		},
		{
			name:    "placeholder the config single quoted is left alone",
			command: `lint '${INPUT}'`,
			want:    `lint '/home/u/my dir/a file (1).ts'`,
		},
		{
			name:    "placeholder inside a longer quoted argument is left alone",
			command: `stylelint --config "${ROOT}/.stylelintrc"`,
			want:    `stylelint --config "/home/u/my dir/.stylelintrc"`,
		},
		// the shapes below are taken verbatim from creativenull/efmls-configs-nvim,
		// where most configs quote the placeholder themselves. every one of them
		// has to come out of here exactly as it went in
		{
			name:    "efm config quoting after an equals sign",
			command: `php-cs-fixer --no-colors --report=emacs --stdin-path="${INPUT}" -`,
			want:    `php-cs-fixer --no-colors --report=emacs --stdin-path="/home/u/my dir/a file (1).ts" -`,
		},
		{
			name:    "efm config piping the placeholder into the tool",
			command: `echo "${INPUT}" | gitlint --format github -S`,
			want:    `echo "/home/u/my dir/a file (1).ts" | gitlint --format github -S`,
		},
		{
			name:    "efm config with a quoted errorformat before the placeholder",
			command: `tidy --nocolor --verbose "%l:%c:%s %m [%p]" "${INPUT}"`,
			want:    `tidy --nocolor --verbose "%l:%c:%s %m [%p]" "/home/u/my dir/a file (1).ts"`,
		},
		{
			name:    "efm config with a single quoted format string before the placeholder",
			command: `mdl --format='{{.LineNumber}}:{{.Violation}}' "${INPUT}"`,
			want:    `mdl --format='{{.LineNumber}}:{{.Violation}}' "/home/u/my dir/a file (1).ts"`,
		},
		{
			name:    "bare placeholder concatenated with a path stays one word",
			command: "${ROOT}/node_modules/.bin/eslint --stdin",
			want:    `'/home/u/my dir'/node_modules/.bin/eslint --stdin`,
		},
		{
			name:    "the file extension is substituted bare",
			command: "prettier --parser ${FILEEXT} --stdin-filepath ${INPUT}",
			want:    `prettier --parser ts --stdin-filepath '/home/u/my dir/a file (1).ts'`,
		},
		{
			name:    "quoting state is tracked across several placeholders",
			command: `lint --root "${ROOT}" --file ${FILENAME}`,
			want:    `lint --root "/home/u/my dir" --file '/home/u/my dir/a file (1).ts'`,
		},
		{
			name:    "a command without placeholders is untouched",
			command: "shellcheck -f gcc -x -",
			want:    "shellcheck -f gcc -x -",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, replaceMagicStrings(tt.command, fname, root))
		})
	}
}

func TestShellQuoteRoundTripsThroughTheShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printf is not a cmd builtin, and the paths below are not valid Windows paths anyway")
	}

	for _, path := range []string{
		"/tmp/plain.ts",
		"/tmp/a file (1).ts",
		"/tmp/it's here.ts",
		"/tmp/$HOME `whoami`.ts",
		`/tmp/back\slash.ts`,
	} {
		t.Run(path, func(t *testing.T) {
			cmd := exec.Command(shell, shellFlag, "printf %s "+shellQuote(path))
			out, err := cmd.Output()

			require.NoError(t, err)
			assert.Equal(t, path, string(out), "the shell must hand the tool exactly one unmangled argument")
		})
	}
}

func TestMatchRootPath(t *testing.T) {
	// root/
	//   marker.toml
	//   sub/
	//     .config/
	//     project.csproj
	//     a.txt
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(sub, ".config"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "marker.toml"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "project.csproj"), nil, 0o600))

	file := filepath.Join(sub, "a.txt")

	tests := []struct {
		name    string
		markers []string
		want    string
	}{
		{"no markers at all", nil, ""},
		{"literal file in the document's own directory", []string{"project.csproj"}, sub},
		{"literal file in an ancestor directory", []string{"marker.toml"}, root},
		{"directory marker", []string{".config/"}, sub},
		{"a directory marker does not match a file", []string{"marker.toml/"}, ""},
		{"a file marker does not match a directory", []string{".config"}, ""},
		{"glob marker", []string{"*.csproj"}, sub},
		{"glob marker in an ancestor directory", []string{"*.toml"}, root},
		{"the closest match wins", []string{"marker.toml", "project.csproj"}, sub},
		{"marker that is nowhere", []string{"nope.json"}, ""},
		{"malformed pattern matches nothing", []string{"["}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, matchRootPath(file, tt.markers))
		})
	}
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
	h := NewHandler(languages)

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
				h.CloseFile(uri)
				require.NoError(t, h.OpenFile(uri, "test", 1, "reopened\n"))
			}
		})
	}

	wg.Go(func() {
		for range rounds {
			h.UpdateConfiguration(&types.Config{Languages: languages})
		}
	})

	wg.Wait()
}
