package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konradmalik/flint-ls/types"
	"github.com/reviewdog/errorformat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLintErrorCases(t *testing.T) {
	tests := []struct {
		name      string
		uri       types.DocumentURI
		expectErr bool
	}{
		{
			name:      "no linter configured",
			uri:       "file:///foo",
			expectErr: false,
		},
		{
			name:      "no such document",
			uri:       "file:///bar",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &LangHandler{
				configs: map[string][]types.Language{},
				files: map[types.DocumentURI]*fileRef{
					types.DocumentURI("file:///foo"): {},
				},
			}

			_, err := h.getAllDiagnosticsForUri(t, tt.uri)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLinting(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	commonFileRef := &fileRef{
		LanguageID:         "vim",
		Text:               "scriptencoding utf-8\nabnormal!\n",
		NormalizedFilename: file,
		Uri:                uri,
	}

	tests := []struct {
		name              string
		langConfig        types.Language
		expectErr         bool
		expectDiagnostics int
		verify            func(t *testing.T, d []types.Diagnostic)
	}{
		{
			name: "NoFileMatched",
			langConfig: types.Language{
				LintCommand:        `echo nofile:2:No it is normal!`,
				LintIgnoreExitCode: true,
				LintStdin:          true,
			},
			expectDiagnostics: 0,
		},
		{
			name: "FileMatched",
			langConfig: types.Language{
				LintCommand:        `echo ` + file + `:2:No it is normal!`,
				LintIgnoreExitCode: true,
				LintStdin:          true,
			},
			expectDiagnostics: 1,
			verify: func(t *testing.T, d []types.Diagnostic) {
				assert.Equal(t, 1, d[0].Range.Start.Line)
				assert.Equal(t, 0, d[0].Range.Start.Character)
				assert.Equal(t, types.DiagnosticSeverity(1), d[0].Severity)
				assert.Equal(t, "No it is normal!", d[0].Message)
			},
		},
		{
			name: "NoIgnoreExitCodeIsRespected",
			langConfig: types.Language{
				LintCommand:        `echo ` + file + `:2:No it is normal!`,
				LintIgnoreExitCode: false,
				LintStdin:          true,
			},
			expectDiagnostics: 0,
		},
		{
			name: "CancelledErrorCodeIsIgnored",
			langConfig: types.Language{
				LintCommand:        `exit -1`,
				LintIgnoreExitCode: true,
				LintStdin:          true,
			},
			expectDiagnostics: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &LangHandler{
				rootPath: base,
				configs: map[string][]types.Language{
					"vim": {tt.langConfig},
				},
				files: map[types.DocumentURI]*fileRef{
					uri: commonFileRef,
				},
			}

			d, err := h.getAllDiagnosticsForUri(t, uri)
			assert.NoError(t, err)
			assert.Len(t, d, tt.expectDiagnostics)

			if tt.verify != nil {
				tt.verify(t, d)
			}
		})
	}
}

func TestDiagnosticsResetOnEachRun(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        `echo ` + file + `:2:No it is normal!`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	pd, err := h.getAllPublishDiagnosticsParamsForUriWithEvent(t, uri, types.EventTypeSave)
	assert.NoError(t, err)

	assert.Len(t, pd, 2)
	assert.Empty(t, pd[0].Diagnostics)
	assert.NotEmpty(t, pd[1].Diagnostics)
}

// TestDiagnosticsOfEveryLinterSurvive covers a document linted by more than one
// configured linter, which is what a wildcard config next to a language one
// gives you. Every publish replaces the client's set for the document, so a
// linter that reported only its own findings would erase the other's.
func TestDiagnosticsOfEveryLinterSurvive(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        `echo ` + file + `:1:from the language linter`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
			types.Wildcard: {
				{
					LintCommand:        `echo ` + file + `:2:from the wildcard linter`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "line one\nline two\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	pd, err := h.getAllPublishDiagnosticsParamsForUriWithEvent(t, uri, types.EventTypeSave)
	assert.NoError(t, err)

	// the reset, then one publish per linter
	require.Len(t, pd, 3)
	assert.Empty(t, pd[0].Diagnostics)
	assert.Len(t, pd[1].Diagnostics, 1, "the first linter to finish reports what it found")

	messages := make([]string, 0, 2)
	for _, d := range pd[2].Diagnostics {
		messages = append(messages, d.Message)
	}
	slices.Sort(messages)
	assert.Equal(t, []string{"from the language linter", "from the wildcard linter"}, messages,
		"the last publish is what the client keeps, so it must hold every linter's findings")
}

// TestSupersededRunPublishesNothing covers a run cancelled mid-flight, which is
// what every keystroke does to the run before it. Its linters are killed, so it
// has nothing left to report -- and publishing that emptiness would wipe out what
// the run that superseded it has already published, because a publish replaces
// the client's whole set for the document. Notifications go out regardless of the
// context, so the run has to hold itself back.
func TestSupersededRunPublishesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the lint command below is written as a POSIX shell command")
	}

	base := t.TempDir()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)
	started := filepath.Join(base, "started")

	// announces that it is running, then waits to be killed
	lint := fmt.Sprintf(`printf x > %s; sleep 30; echo 1:too late`, started)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        lint,
					LintFormats:        []string{"%l:%m"},
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "line one\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reporter := &recordingReporter{}
	done := make(chan error, 1)
	go func() { done <- h.RunAllLinters(ctx, reporter, uri, types.EventTypeChange) }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, 10*time.Second, time.Millisecond, "the linter never started")

	cancel()
	require.NoError(t, <-done)

	published := reporter.publishedDiagnostics()
	require.Len(t, published, 1, "a cancelled run must publish nothing beyond its initial reset")
	assert.Empty(t, published[0].Diagnostics)
}

// TestLintPathNeedingQuoting covers a filename that the shell would mangle if it
// were pasted into the command bare.
func TestLintPathNeedingQuoting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the lint command below is written as a POSIX shell command")
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "a file (1) 'quoted'.txt")
	require.NoError(t, os.WriteFile(file, []byte("hello"), 0o600))
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: dir,
		configs: map[string][]types.Language{
			"vim": {
				{
					// reads the file it is given, so it only works if the path
					// reaches it as a single argument
					LintCommand:        "echo 1:$(cat ${INPUT})",
					LintFormats:        []string{"%l:%m"},
					LintIgnoreExitCode: true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "hello",
				NormalizedFilename: filepath.ToSlash(file),
				Uri:                uri,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri)
	require.NoError(t, err)
	require.Len(t, d, 1)
	assert.Equal(t, "hello", d[0].Message)
}

func TestLintFileMatchedWildcard(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			types.Wildcard: {
				{
					LintCommand:        `echo ` + file + `:2:No it is normal!`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri)
	assert.NoError(t, err)

	assert.Len(t, d, 1)
	assert.Equal(t, d[0].Range.Start.Line, 1)
	assert.Equal(t, d[0].Range.Start.Character, 0)
	assert.Equal(t, d[0].Severity, types.DiagnosticSeverity(1))
	assert.Equal(t, d[0].Message, "No it is normal!")
}

// column 0 remains unchanged, regardless of the configured offset
// column 0 indicates a whole line (although for 0-based column linters we can not distinguish between word starting at 0 and the whole line)
func TestLintOffsetColumns(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	tests := []struct {
		name              string
		lintOffsetColumns int
		inputColumn       string
		expectedCharacter int
		description       string
	}{
		{
			name:              "zero column remains unchanged",
			lintOffsetColumns: 1,
			inputColumn:       "0",
			expectedCharacter: 0,
			description:       "column 0 remains unchanged, regardless of the configured offset",
		},
		{
			name:              "no offset assumes 1-based",
			lintOffsetColumns: 0,
			inputColumn:       "1",
			expectedCharacter: 0,
			description:       "without column offset, 1-based columns are assumed, which means that we should get 0 for column 1 as LSP assumes 0-based columns",
		},
		{
			name:              "with offset preserves column",
			lintOffsetColumns: 1,
			inputColumn:       "1",
			expectedCharacter: 1,
			description:       "for column 1 with offset we should get column 1 back - without the offset efm would subtract 1 as it expects 1 based columns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &LangHandler{
				rootPath: base,
				configs: map[string][]types.Language{
					types.Wildcard: {
						{
							LintCommand:        `echo ` + file + `:2:` + tt.inputColumn + `:msg`,
							LintFormats:        []string{"%f:%l:%c:%m"},
							LintIgnoreExitCode: true,
							LintStdin:          true,
							LintOffsetColumns:  tt.lintOffsetColumns,
						},
					},
				},
				files: map[types.DocumentURI]*fileRef{
					uri: {
						LanguageID:         "vim",
						Text:               "scriptencoding utf-8\nabnormal!\n",
						NormalizedFilename: file,
						Uri:                uri,
					},
				},
			}

			d, err := h.getAllDiagnosticsForUri(t, uri)
			assert.NoError(t, err)

			assert.Len(t, d, 1)
			assert.Equal(t, tt.expectedCharacter, d[0].Range.Start.Character)
		})
	}
}

func TestLintCategoryMap(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	mapping := make(map[string]string)
	mapping["R"] = "I" // pylint refactoring to info

	formats := []string{"%f:%l:%c:%t:%m"}

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			types.Wildcard: {
				{
					LintCommand:        `echo ` + file + `:2:1:R:No it is normal!`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
					LintFormats:        formats,
					LintCategoryMap:    mapping,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri)
	assert.NoError(t, err)

	assert.Len(t, d, 1)
	assert.Equal(t, d[0].Severity, types.DiagnosticSeverity(3))
}

// Test if lint is executed if required root markers for the language are missing
func TestLintRequireRootMarker(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        `echo ` + file + `:2:No it is normal!`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
					RequireMarker:      true,
					RootMarkers:        []string{".vimlintrc"},
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri)
	assert.NoError(t, err)

	assert.Empty(t, d)
}

func TestLintSingleEntry(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	file2 := filepath.Join(base, "bar")
	uri := ParseLocalFileToURI(file)
	uri2 := ParseLocalFileToURI(file2)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        `echo ` + file + `:2:1:First file! && echo ` + file2 + `:1:2:Second file!`,
					LintFormats:        []string{"%f:%l:%c:%m"},
					LintIgnoreExitCode: true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
			uri2: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file2,
				Uri:                uri2,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri)
	assert.NoError(t, err)

	assert.Len(t, d, 1)
	assert.Equal(t, d[0].Range.Start.Line, 1)
	assert.Equal(t, d[0].Range.Start.Character, 0)
}

func TestLintMultipleEntries(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	file2 := filepath.Join(base, "bar")
	uri := ParseLocalFileToURI(file)
	uri2 := ParseLocalFileToURI(file2)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        `echo ` + file + `:2:1:First file! && echo ` + file2 + `:2:3:Second file! && echo ` + file2 + `:Empty l and c!`,
					LintFormats:        []string{"%f:%l:%c:%m", "%f:%m"},
					LintIgnoreExitCode: true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
			uri2: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file2,
				Uri:                uri2,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri2)
	assert.NoError(t, err)

	assert.Len(t, d, 2)
	assert.Equal(t, d[0].Range.Start.Line, 1)
	assert.Equal(t, d[0].Range.Start.Character, 2)
	assert.Equal(t, d[1].Range.Start.Line, 0)
	assert.Equal(t, d[1].Range.Start.Character, 0)
}

func TestLintNoDiagnostics(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        "echo ",
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	d, err := h.getAllDiagnosticsForUri(t, uri)
	assert.NoError(t, err)

	assert.Empty(t, d)
}

func TestLintEventTypes(t *testing.T) {
	base, _ := os.Getwd()
	file := filepath.Join(base, "foo")
	uri := ParseLocalFileToURI(file)

	h := &LangHandler{
		rootPath: base,
		configs: map[string][]types.Language{
			"vim": {
				{
					LintCommand:        `echo ` + file + `:2:No it is normal!`,
					LintIgnoreExitCode: true,
					LintStdin:          true,
				},
			},
		},
		files: map[types.DocumentURI]*fileRef{
			uri: {
				LanguageID:         "vim",
				Text:               "scriptencoding utf-8\nabnormal!\n",
				NormalizedFilename: file,
				Uri:                uri,
			},
		},
	}

	tests := []struct {
		name           string
		event          types.EventType
		lintAfterOpen  bool
		lintOnSave     bool
		lintOnChange   bool
		expectMessages int
	}{
		{
			name:           "LintOnOpen true",
			event:          types.EventTypeOpen,
			lintAfterOpen:  true,
			expectMessages: 1,
		},
		{
			name:           "LintOnOpen false",
			event:          types.EventTypeOpen,
			lintAfterOpen:  false,
			expectMessages: 0,
		},
		{
			name:           "LintOnChange true",
			event:          types.EventTypeChange,
			lintOnChange:   true,
			expectMessages: 1,
		},
		{
			name:           "LintOnChange false",
			event:          types.EventTypeChange,
			lintOnChange:   false,
			expectMessages: 0,
		},
		{
			name:           "LintOnSave true",
			event:          types.EventTypeSave,
			lintOnSave:     true,
			expectMessages: 1,
		},
		{
			name:           "LintOnSave false",
			event:          types.EventTypeSave,
			lintOnSave:     false,
			expectMessages: 0,
		},
		// a run covering more than one event is what a schedule that superseded an
		// earlier one produces: it has to lint for a config that wants either
		{
			name:           "combined events, config wants only one of them",
			event:          types.EventTypeSave | types.EventTypeChange,
			lintOnSave:     true,
			lintOnChange:   false,
			expectMessages: 1,
		},
		{
			name:           "combined events, config wants neither",
			event:          types.EventTypeSave | types.EventTypeChange,
			lintOnSave:     false,
			lintOnChange:   false,
			expectMessages: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.configs["vim"][0].LintAfterOpen = new(tt.lintAfterOpen)
			h.configs["vim"][0].LintOnChange = new(tt.lintOnChange)
			h.configs["vim"][0].LintOnSave = new(tt.lintOnSave)
			d, err := h.getAllDiagnosticsForUriWithEvent(t, uri, tt.event)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectMessages, len(d))
		})
	}
}

func TestGetSeverity(t *testing.T) {
	tests := []struct {
		name            string
		typ             rune
		categoryMap     map[string]string
		defaultSeverity types.DiagnosticSeverity
		want            types.DiagnosticSeverity
	}{
		{"Error type", 'E', nil, 0, types.DiagError},
		{"Warning type", 'W', nil, 0, types.DiagWarning},
		{"Info type", 'I', nil, 0, types.DiagInformation},
		{"Hint type", 'N', nil, 0, types.DiagHint},
		{"Default severity overrides", 'X', nil, types.DiagWarning, types.DiagWarning},
		{"Category map remap", 'X', map[string]string{"X": "W"}, 0, types.DiagWarning},
		// a config that maps only some of its linter's categories is fine: the
		// ones it says nothing about keep what the linter reported
		{"Category map without the reported type", 'W', map[string]string{"R": "I"}, 0, types.DiagWarning},
		{"Category map without the reported type falls back to the default", 'X', map[string]string{"R": "I"}, types.DiagHint, types.DiagHint},
		{"Category map with an empty mapping", 'W', map[string]string{"W": ""}, 0, types.DiagWarning},
		{"No type reported at all", 0, map[string]string{"R": "I"}, 0, types.DiagError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getSeverity(tt.typ, tt.categoryMap, tt.defaultSeverity)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsEntryForRequestedURI(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		uri      string
		entry    *errorformat.Entry
		expected bool
	}{
		{
			name: "main dir",
			root: "/home/torvalds/linux/",
			uri:  "file:///home/torvalds/linux/main.go",
			entry: &errorformat.Entry{
				Filename: "main.go",
			},
			expected: true,
		},
		{
			name: "subdir without slash",
			root: "/home/torvalds/linux/",
			uri:  "file:///home/torvalds/linux/gpu/nvidia/driver.go",
			entry: &errorformat.Entry{
				Filename: "gpu/nvidia/driver.go",
			},
			expected: true,
		},
		{
			name: "subdir with slash is absolute",
			root: "/home/torvalds/linux/",
			uri:  "file:///home/torvalds/linux/gpu/nvidia/driver.go",
			entry: &errorformat.Entry{
				Filename: "/gpu/nvidia/driver.go",
			},
			expected: runtime.GOOS == "windows",
		},
		{
			name: "empty filename is accepted",
			root: "/home/torvalds/linux/",
			uri:  "file:///home/torvalds/linux/gpu/nvidia/driver.go",
			entry: &errorformat.Entry{
				Filename: "",
			},
			expected: true,
		},
		{
			name: "comparison is case sensitive",
			root: "/home/torvalds/linux/",
			uri:  "file:///home/torvalds/linux/main.go",
			entry: &errorformat.Entry{
				Filename: "Main.go",
			},
			expected: runtime.GOOS == "windows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := isEntryForRequestedURI(tt.root, types.DocumentURI(tt.uri), tt.entry)
			assert.Equal(t, tt.expected, ok)
		})
	}
}

func TestParseEfmEntryToDiagnostic(t *testing.T) {
	file := &fileRef{Text: "hello world\ngolang rulezz", LanguageID: "txt"}
	tests := []struct {
		name     string
		entry    *errorformat.Entry
		cfg      *types.Language
		expected types.Diagnostic
	}{
		{
			name: "first line as 1, word",
			entry: &errorformat.Entry{
				Lnum: 1,
				Col:  7,
				Text: "world bad",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 0,
			},
			expected: types.Diagnostic{
				Message:  "world bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 0, Character: 6},
					End:   types.Position{Line: 0, Character: 11},
				},
			},
		},
		{
			name: "first line as 0, word",
			entry: &errorformat.Entry{
				Lnum: 0,
				Col:  7,
				Text: "world bad",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 0,
			},
			expected: types.Diagnostic{
				Message:  "world bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 0, Character: 6},
					End:   types.Position{Line: 0, Character: 11},
				},
			},
		},
		{
			name: "second line, word",
			entry: &errorformat.Entry{
				Lnum: 2,
				Col:  1,
				Text: "golang bad",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 0,
			},
			expected: types.Diagnostic{
				Message:  "golang bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 1, Character: 0},
					End:   types.Position{Line: 1, Character: 6},
				},
			},
		},
		{
			name: "second line, whole",
			entry: &errorformat.Entry{
				Lnum: 2,
				Col:  0,
				Text: "golang not rulezz",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 0,
			},
			expected: types.Diagnostic{
				Message:  "golang not rulezz",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 1, Character: 0},
					End:   types.Position{Line: 1, Character: 0},
				},
			},
		},
		{
			name: "line offset is subtracted",
			entry: &errorformat.Entry{
				Lnum: 1,
				Col:  7,
				Text: "world bad",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        -1,
				LintOffsetColumns: 0,
			},
			expected: types.Diagnostic{
				Message:  "world bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 1, Character: 6},
					End:   types.Position{Line: 1, Character: 7},
				},
			},
		},
		{
			name: "col offset is added",
			entry: &errorformat.Entry{
				Lnum: 1,
				Col:  7,
				Text: "world bad",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 1,
			},
			expected: types.Diagnostic{
				Message:  "world bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 0, Character: 7},
					End:   types.Position{Line: 0, Character: 12},
				},
			},
		},
		{
			name: "col offset is not added if whole line",
			entry: &errorformat.Entry{
				Lnum: 1,
				Col:  0,
				Text: "world bad",
				Type: 'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 11,
			},
			expected: types.Diagnostic{
				Message:  "world bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 0, Character: 0},
					End:   types.Position{Line: 0, Character: 0},
				},
			},
		},
		{
			name: "multiline is handled",
			entry: &errorformat.Entry{
				Lnum:    1,
				EndLnum: 3,
				Col:     0,
				Text:    "bad",
				Type:    'E',
			},
			cfg: &types.Language{
				LintOffset:        -2,
				LintOffsetColumns: 0,
			},
			expected: types.Diagnostic{
				Message:  "bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 2, Character: 0},
					End:   types.Position{Line: 4, Character: 0},
				},
			},
		},
		{
			name: "multicol is handled",
			entry: &errorformat.Entry{
				Lnum:    2,
				EndLnum: 2,
				Col:     3,
				EndCol:  7,
				Text:    "bad",
				Type:    'E',
			},
			cfg: &types.Language{
				LintOffset:        0,
				LintOffsetColumns: 2,
			},
			expected: types.Diagnostic{
				Message:  "bad",
				Severity: types.DiagError,
				Range: types.Range{
					Start: types.Position{Line: 1, Character: 4},
					End:   types.Position{Line: 1, Character: 8},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := parseEfmEntryToDiagnostic(tt.entry, *tt.cfg, *file)
			assert.Equal(t, tt.expected.Message, diag.Message)
			assert.Equal(t, tt.expected.Severity, diag.Severity)
			assert.Equal(t, tt.expected.Range.Start.Line, diag.Range.Start.Line)
			assert.Equal(t, tt.expected.Range.Start.Character, diag.Range.Start.Character)
			assert.Equal(t, tt.expected.Range.End.Line, diag.Range.End.Line)
			assert.Equal(t, tt.expected.Range.End.Character, diag.Range.End.Character)
		})
	}
}

func (h *LangHandler) getAllDiagnosticsForUri(t *testing.T, uri types.DocumentURI) ([]types.Diagnostic, error) {
	return h.getAllDiagnosticsForUriWithEvent(t, uri, types.EventTypeChange)
}

func (h *LangHandler) getAllDiagnosticsForUriWithEvent(t *testing.T, uri types.DocumentURI, event types.EventType) ([]types.Diagnostic, error) {
	params, err := h.getAllPublishDiagnosticsParamsForUriWithEvent(t, uri, event)
	diagnostics := make([]types.Diagnostic, 0)
	for _, p := range params {
		diagnostics = append(diagnostics, p.Diagnostics...)
	}
	return diagnostics, err
}

func (h *LangHandler) getAllPublishDiagnosticsParamsForUriWithEvent(t *testing.T, uri types.DocumentURI, event types.EventType) ([]types.PublishDiagnosticsParams, error) {
	reporter := &recordingReporter{}

	runErr := h.RunAllLinters(t.Context(), reporter, uri, event)

	errs := reporter.errorMessages()
	if runErr != nil {
		errs = append(errs, runErr.Error())
	}

	if len(errs) != 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, ";"))
	}
	return reporter.publishedDiagnostics(), nil
}

// recordingReporter is a core.Reporter that accumulates everything reported to
// it. Linters report from several goroutines at once, so it locks.
type recordingReporter struct {
	mu          sync.Mutex
	diagnostics []types.PublishDiagnosticsParams
	progress    []types.ProgressParams
	errors      []error
}

func (r *recordingReporter) PublishDiagnostics(_ context.Context, params types.PublishDiagnosticsParams) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.diagnostics = append(r.diagnostics, params)
}

func (r *recordingReporter) Progress(_ context.Context, params types.ProgressParams) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, params)
}

func (r *recordingReporter) ReportError(_ context.Context, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, err)
}

func (r *recordingReporter) publishedDiagnostics() []types.PublishDiagnosticsParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.diagnostics)
}

func (r *recordingReporter) progressEvents() []types.ProgressParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.progress)
}

func (r *recordingReporter) errorMessages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	messages := make([]string, 0, len(r.errors))
	for _, err := range r.errors {
		messages = append(messages, err.Error())
	}
	return messages
}
