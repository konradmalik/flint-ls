package core

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
	"github.com/reviewdog/errorformat"
)

var unknownExitCode = -999
var defaultLintFormats = []string{"%f:%l:%m", "%f:%l:%c:%m"}

func (h *LangHandler) RunAllLinters(
	ctx context.Context, uri types.DocumentURI, eventType types.EventType,
	diagnosticsOut chan<- types.PublishDiagnosticsParams,
	errorsOut chan<- error,
	progress chan<- types.ProgressParams) error {
	f, ok := h.files[uri]
	if !ok {
		return fmt.Errorf("document not found: %v", uri)
	}

	configs := getLintConfigsForDocument(f.NormalizedFilename, f.LanguageID, h.configs, eventType)
	if len(configs) == 0 {
		logs.Log.Logf(logs.Debug, "no matching lint configs for LanguageID: %v", f.LanguageID)
		return nil
	}

	// to reset existing
	diagnosticsOut <- types.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: make([]types.Diagnostic, 0),
		Version:     f.Version,
	}

	progressToken := types.NewProgressToken()
	progress <- types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressBegin("Linting document", nil, nil),
	}

	var wg sync.WaitGroup
	for _, config := range configs {
		wg.Go(func() {
			rootPath := h.findRootPath(f.NormalizedFilename, config)
			diagnostics, err := lintDocument(ctx, rootPath, *f, config)
			if err != nil {
				logs.Log.Logln(logs.Error, err.Error())
				errorsOut <- err
				return
			}

			diagnosticsOut <- types.PublishDiagnosticsParams{
				URI:         uri,
				Diagnostics: diagnostics,
				Version:     f.Version,
			}
		})
	}

	wg.Wait()

	progress <- types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressEnd(nil),
	}

	return nil
}

func lintDocument(ctx context.Context, rootPath string, f fileRef, config types.Language) ([]types.Diagnostic, error) {
	diagnostics := make([]types.Diagnostic, 0)
	cmdStr := buildLintCommandString(rootPath, f, config)
	cmd := buildExecCmd(ctx, cmdStr, rootPath, f.Text, config, config.LintStdin)

	lintOutput, err := runLintCommand(cmd, &config)
	logs.Log.Logln(logs.Info, cmdStr)
	logs.Log.Logln(logs.Debug, string(lintOutput))
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

		entry.Filename = replaceStdinInEntryFilename(entry.Filename, &config, f.NormalizedFilename)
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
	// we allow the config to provide a mapping between LSP types E,W,I,N and whatever categories the linter has
	if len(categoryMap) > 0 {
		typ = []rune(categoryMap[string(typ)])[0]
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

func getLintConfigsForDocument(fname, langId string, allConfigs map[string][]types.Language, eventType types.EventType) []types.Language {
	var configs []types.Language
	for _, cfg := range getAllConfigsForLang(allConfigs, langId) {
		if cfg.LintCommand == "" {
			continue
		}
		// if we require markers and find that they dont exist we do not add the configuration
		if dir := matchRootPath(fname, cfg.RootMarkers); dir == "" && cfg.RequireMarker {
			continue
		}
		switch eventType {
		case types.EventTypeOpen:
			if !boolOrDefault(cfg.LintAfterOpen, true) {
				continue
			}
		case types.EventTypeChange:
			if !boolOrDefault(cfg.LintOnChange, true) {
				continue
			}
		case types.EventTypeSave:
			if !boolOrDefault(cfg.LintOnSave, true) {
				continue
			}
		default:
		}
		configs = append(configs, cfg)
	}
	return configs
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

func runLintCommand(cmd *exec.Cmd, config *types.Language) ([]byte, error) {
	lintOutput, lintCmdError := cmd.CombinedOutput()

	isExitCode0 := lintCmdError == nil
	if isExitCode0 {
		// LintIgnoreExitCode means despite lint returning 0, we still parse for errors
		if config.LintIgnoreExitCode {
			return lintOutput, nil
		}
		return nil, nil
	}

	code := parseErrorExitCode(lintCmdError)
	if code == unknownExitCode {
		return lintOutput, lintCmdError
	}

	if code < 0 {
		// In go, anything < 0 means some interrupt (canceled, killed etc.)
		return nil, nil
	}

	return lintOutput, nil
}

func parseErrorExitCode(err error) int {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return unknownExitCode
	}
	return exitErr.ExitCode()
}

func replaceStdinInEntryFilename(entryFilename string, config *types.Language, fname string) string {
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
		lineEnd = max(entry.EndLnum-1-config.LintOffset, 0)
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
		Code:     itoaPtrIfNotZero(entry.Nr),
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
