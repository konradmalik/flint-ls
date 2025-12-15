package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/konradmalik/flint-ls/types"
	"github.com/stretchr/testify/assert"
)

func TestNormalizedFilenameFromURI(t *testing.T) {
	uri := types.DocumentURI("file:///tmp/TestFile.txt")
	fname, err := normalizedFilenameFromUri(uri)
	assert.NoError(t, err)
	assert.Equal(t, "/tmp/TestFile.txt", fname)
}

func TestApplyOptionsPlaceholders_DefaultTypes(t *testing.T) {
	cmd := "echo ${--flag:opt} ${--flag2=opt}"
	opts := types.FormattingOptions{
		"opt": "value",
	}
	out, err := applyOptionsPlaceholders(cmd, opts)
	assert.NoError(t, err)
	assert.Contains(t, out, "--flag value")
	assert.Contains(t, out, "--flag2=value")
}

func TestApplyOptionsPlaceholders_BoolTrue(t *testing.T) {
	cmd := "echo ${--flag:opt} ${--flag2=opt}"
	opts := types.FormattingOptions{
		"opt": true,
	}
	out, err := applyOptionsPlaceholders(cmd, opts)
	assert.NoError(t, err)
	assert.Equal(t, "echo --flag --flag2", out)
}

func TestApplyOptionsPlaceholders_BoolFalse(t *testing.T) {
	cmd := "echo ${--flag:!opt} ${--flag2=!opt}"
	opts := types.FormattingOptions{
		"opt": false,
	}
	out, err := applyOptionsPlaceholders(cmd, opts)
	assert.NoError(t, err)
	assert.Equal(t, "echo --flag --flag2", out)
}

func TestApplyRangePlaceholders(t *testing.T) {
	cmd := "echo ${--flag:charStart} ${--flag=charEnd}"
	rng := &types.Range{
		Start: types.Position{Line: 0, Character: 2},
		End:   types.Position{Line: 0, Character: 4},
	}
	text := "abcdef"
	out, err := applyRangePlaceholders(cmd, rng, text)
	assert.NoError(t, err)
	assert.Contains(t, out, "--flag 2")
	assert.Contains(t, out, "--flag=4")
}

func TestBuildCommand_HandlesPlaceholders(t *testing.T) {
	command := "echo ${flag:opt} ${anotherflag:tpo}"
	opts := types.FormattingOptions{"opt": "value"}

	cmdStr, err := buildFormatCommandString("/root", "file.txt", "text", opts, nil, command)

	assert.NoError(t, err)

	assert.Contains(t, cmdStr, "flag value")
	assert.NotContains(t, cmdStr, "anotherflag")
	assert.NotContains(t, cmdStr, "file.txt")
}

func TestFormatDocument_WithStdin(t *testing.T) {
	cfg := types.Language{FormatCommand: "cat -"}
	tmpDir := t.TempDir()

	out, err := formatDocument(t.Context(), tmpDir, "file.txt", "hello text", nil, nil, cfg)

	assert.NoError(t, err)
	assert.Equal(t, "hello text", strings.TrimSpace(out))
}

func TestRunFormatters_Success(t *testing.T) {
	tmpDir := t.TempDir()
	testfile := filepath.Join(tmpDir, "text.txt")
	err := os.WriteFile(testfile, []byte("test"), 0755)
	assert.NoError(t, err)

	h := &LangHandler{
		files: map[types.DocumentURI]*fileRef{
			types.DocumentURI("file://" + testfile): {Text: "hello", LanguageID: "go", NormalizedFilename: testfile},
		},
		configs: map[string][]types.Language{
			"go": {{FormatCommand: "cat", RequireMarker: false}},
		},
	}
	edits, err := runAllFormatters(t, h, types.DocumentURI("file://"+testfile))
	assert.NoError(t, err)
	assert.NotNil(t, edits)
}

func TestRunFormatters_UsesPreviousText(t *testing.T) {
	tmpDir := t.TempDir()
	testfile := filepath.Join(tmpDir, "text.txt")
	err := os.WriteFile(testfile, []byte("test"), 0755)
	assert.NoError(t, err)
	var cmd1 string
	var cmd2 string
	if runtime.GOOS == "windows" {
		cmd1 = "set /p line= && call echo %line%config1"
		cmd2 = "set /p line= && call echo %line%config2"
	} else {
		cmd1 = "echo \"$(cat -)config1\""
		cmd2 = "echo \"$(cat -)config2\""
	}

	h := &LangHandler{
		files: map[types.DocumentURI]*fileRef{
			types.DocumentURI("file://" + testfile): {Text: "hello", LanguageID: "go", NormalizedFilename: testfile},
		},
		configs: map[string][]types.Language{
			"go": {
				{FormatCommand: cmd1, RequireMarker: false},
				{FormatCommand: cmd2, RequireMarker: false},
			},
		},
	}
	edits, err := runAllFormatters(t, h, types.DocumentURI("file://"+testfile))
	assert.NoError(t, err)
	assert.Equal(t, "helloconfig1config2\n", edits[0].NewText)
}

func TestRunFormatters_RequireRootMatcher(t *testing.T) {
	base, _ := os.Getwd()
	filepath := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(filepath)

	h := &LangHandler{
		RootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					FormatCommand: `echo ` + filepath + `:2:No it is normal!`,
					RequireMarker: true,
					RootMarkers:   []string{".vimfmtrc"},
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID: "vim",
				Text:       "scriptencoding utf-8\nabnormal!\n",
			},
		},
	}

	edits, err := runAllFormatters(t, h, uri)
	assert.NoError(t, err)
	assert.Empty(t, edits)
}

func runAllFormatters(t *testing.T, h *LangHandler, uri types.DocumentURI) ([]types.TextEdit, error) {
	progress := blackHoleProgress()
	defer close(progress)
	return h.RunAllFormatters(t.Context(), uri, nil, types.FormattingOptions{}, progress)
}
