package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
	"github.com/reviewdog/errorformat"
)

var defaultLintFormats = []string{"%f:%l:%m", "%f:%l:%c:%m"}

// RunAllLinters lints uri with every configured linter that applies to
// eventType, reporting diagnostics as each linter finishes. It blocks until all
// linters are done or ctx is cancelled.
func (h *LangHandler) RunAllLinters(ctx context.Context, reporter Reporter, uri types.DocumentURI, eventType types.EventType) error {
	snap, err := h.snapshot(uri)
	if err != nil {
		return err
	}
	f := snap.file

	configs := resolveConfigs(f.NormalizedFilename, f.LanguageID, snap.rootPath, snap.configs,
		func(cfg types.Language) bool { return cfg.LintCommand != "" && lintsOnEvent(cfg, eventType) })
	if len(configs) == 0 {
		logs.Log.Logf(logs.Debug, "no matching lint configs for LanguageID: %v", f.LanguageID)
		return nil
	}

	// to reset existing
	reporter.PublishDiagnostics(ctx, types.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: make([]types.Diagnostic, 0),
		Version:     f.Version,
	})

	progressToken := types.NewProgressToken()
	reporter.Progress(ctx, types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressBegin("Linting document", nil, nil),
	})
	// deferred so that an early return can never leave the client with a
	// progress token that is begun but never ended
	defer reporter.Progress(ctx, types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressEnd(nil),
	})

	// every publish replaces the client's whole set for the document, so each
	// linter reports the union of what has finished so far rather than only its
	// own findings -- otherwise the linters would erase each other. mu is held
	// across the publish as well, which keeps the sets the client sees growing
	// monotonically instead of letting a smaller one overtake a larger one.
	var mu sync.Mutex
	published := make([]types.Diagnostic, 0)

	var wg sync.WaitGroup
	for _, config := range configs {
		wg.Go(func() {
			diagnostics, err := lintDocument(ctx, config.rootPath, f, config.Language)
			if err != nil {
				logs.Log.Logln(logs.Error, err.Error())
				reporter.ReportError(ctx, err)
				return
			}

			mu.Lock()
			defer mu.Unlock()

			published = append(published, diagnostics...)
			reporter.PublishDiagnostics(ctx, types.PublishDiagnosticsParams{
				URI:         uri,
				Diagnostics: published,
				Version:     f.Version,
			})
		})
	}

	wg.Wait()

	return nil
}

func lintDocument(ctx context.Context, rootPath string, f fileRef, config types.Language) ([]types.Diagnostic, error) {
	diagnostics := make([]types.Diagnostic, 0)
	cmdStr := buildLintCommandString(rootPath, f, config)

	var stdin io.Reader
	if config.LintStdin {
		stdin = strings.NewReader(f.Text)
	}
	cmd := buildExecCmd(ctx, cmdStr, rootPath, config.Env, stdin)

	lintOutput, err := runLintCommand(cmd, config)
	logs.Log.Logln(logs.Info, cmdStr)
	if logs.Log.Enabled(logs.Debug) {
		// the output can be large, so it is only copied into a string when
		// something is actually going to read it
		logs.Log.Logln(logs.Debug, string(lintOutput))
	}
	if err != nil {
		return nil, err
	}

	efms, err := buildErrorformats(config.LintFormats)
	if err != nil {
		return nil, err
	}

	efmsScanner := efms.NewScanner(bytes.NewReader(lintOutput))
	for efmsScanner.Scan() {
		entry := efmsScanner.Entry()
		if !entry.Valid {
			continue
		}

		entry.Filename = replaceStdinInEntryFilename(entry.Filename, config, f.NormalizedFilename)
		if !isEntryForRequestedURI(rootPath, f.Uri, entry) {
			// entry for a different file, skip
			continue
		}

		diagnostic := parseEfmEntryToDiagnostic(entry, config, f)
		diagnostics = append(diagnostics, diagnostic)
	}

	return diagnostics, nil
}

func getSeverity(typ rune, categoryMap map[string]string, defaultSeverity types.DiagnosticSeverity) types.DiagnosticSeverity {
	// we allow the config to provide a mapping between LSP types E,W,I,N and whatever categories the linter has.
	// a category the config does not mention keeps whatever the linter reported: a partial mapping is a
	// perfectly reasonable config, and it must not decide the severity of categories it says nothing about
	if mapped, ok := categoryMap[string(typ)]; ok && mapped != "" {
		typ = []rune(mapped)[0]
	}

	severity := types.DiagError
	if defaultSeverity != 0 {
		severity = defaultSeverity
	}

	switch typ {
	case 'E', 'e':
		severity = types.DiagError
	case 'W', 'w':
		severity = types.DiagWarning
	case 'I', 'i':
		severity = types.DiagInformation
	case 'N', 'n':
		severity = types.DiagHint
	}
	return severity
}

// lintsOnEvent reports whether cfg wants to run for this kind of event. Linting
// on open, change and save is all on unless the config turns it off.
func lintsOnEvent(cfg types.Language, eventType types.EventType) bool {
	switch eventType {
	case types.EventTypeOpen:
		return boolOrDefault(cfg.LintAfterOpen, true)
	case types.EventTypeChange:
		return boolOrDefault(cfg.LintOnChange, true)
	case types.EventTypeSave:
		return boolOrDefault(cfg.LintOnSave, true)
	default:
		return true
	}
}

func buildErrorformats(configFormats []string) (*errorformat.Errorformat, error) {
	if len(configFormats) == 0 {
		configFormats = defaultLintFormats
	}

	efms, err := errorformat.NewErrorformat(configFormats)
	if err != nil {
		return nil, fmt.Errorf("invalid error-format: %v", configFormats)
	}
	return efms, nil
}

func buildLintCommandString(rootPath string, f fileRef, config types.Language) string {
	command := config.LintCommand
	if !config.LintStdin && !strings.Contains(command, inputPlaceholder) {
		command = command + " " + inputPlaceholder
	}
	return replaceMagicStrings(command, f.NormalizedFilename, rootPath)
}

// runLintCommand runs a linter and returns the output that is worth parsing for
// diagnostics, which is not the same thing as everything it printed.
func runLintCommand(cmd *exec.Cmd, config types.Language) ([]byte, error) {
	lintOutput, lintCmdError := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	switch {
	case lintCmdError == nil:
		// the linter found nothing to complain about -- unless the config says
		// this linter exits 0 even when it did have something to say
		if config.LintIgnoreExitCode {
			return lintOutput, nil
		}
		return nil, nil
	case !errors.As(lintCmdError, &exitErr):
		// the linter never ran, or something failed that is not the linter
		// telling us about the document
		return lintOutput, lintCmdError
	case exitErr.ExitCode() < 0:
		// killed rather than exited: superseded by a newer run, or shutting down
		return nil, nil
	default:
		// a non-zero exit is how a linter reports that it found something
		return lintOutput, nil
	}
}

func replaceStdinInEntryFilename(entryFilename string, config types.Language, fname string) string {
	if config.LintStdin && isStdinPlaceholder(entryFilename) {
		entryFilename = fname
	}
	return filepath.ToSlash(entryFilename)
}

func isEntryForRequestedURI(rootPath string, uri types.DocumentURI, entry *errorformat.Entry) bool {
	// if entry.Filename is empty, we simply assume it's for this file
	if entry.Filename == "" {
		return true
	}
	// if entry.Filename is not empty, we need to check if this entry is indeed for this uri
	var diagURI types.DocumentURI
	if filepath.IsAbs(entry.Filename) {
		diagURI = ParseLocalFileToURI(entry.Filename)
	} else {
		diagURI = ParseLocalFileToURI(filepath.Join(rootPath, entry.Filename))
	}
	return comparePaths(string(diagURI), string(uri))
}

func parseEfmEntryToDiagnostic(entry *errorformat.Entry, config types.Language, f fileRef) types.Diagnostic {
	// vast majority of linters report 1-based lines and columns, but lsp requires 0-based
	// BUG: LintOffset should be added, not subtracted. But to keep backwards compatibility let's leave this bug here
	lineStart := max(entry.Lnum-1-config.LintOffset, 0)
	lineEnd := lineStart
	if entry.EndLnum != 0 {
		// a linter that reports an end before the start would give the client a
		// range it cannot highlight, so the end never precedes the start
		lineEnd = max(entry.EndLnum-1-config.LintOffset, lineStart)
	}

	colStart := max(entry.Col-1, 0)
	colEnd := colStart

	// entry.Col is expected to be one based
	// if the linter reports 0 it means the whole line
	if entry.Col != 0 {
		// We only add the offset if the linter reports entry.Col > 0 because 0 means the whole line
		colStart = colStart + config.LintOffsetColumns

		if entry.EndCol != 0 {
			colEnd = max(entry.EndCol-1, 0)
			colEnd = colEnd + config.LintOffsetColumns
			if lineEnd == lineStart {
				// on a single line the end column has to follow the start one;
				// across lines a smaller end column is perfectly normal
				colEnd = max(colEnd, colStart)
			}
		} else {
			word := WordAtUtf16(f.Text, types.Position{Line: lineStart, Character: colStart})
			colEnd = colStart + len(word)
		}
	}

	return types.Diagnostic{
		Range: types.Range{
			Start: types.Position{Line: lineStart, Character: colStart},
			End:   types.Position{Line: lineEnd, Character: colEnd},
		},
		Code:     intPtrIfNotZero(entry.Nr),
		Message:  getLintMessagePrefix(config) + entry.Text,
		Severity: getSeverity(entry.Type, config.LintCategoryMap, config.LintSeverity),
		Source:   getLintSource(config),
	}
}

func getLintSource(config types.Language) *string {
	if config.LintSource != "" {
		return &config.LintSource
	}
	return nil
}

func getLintMessagePrefix(config types.Language) string {
	var prefix string
	if config.Prefix != "" {
		prefix = fmt.Sprintf("[%s] ", config.Prefix)
	}
	return prefix
}
