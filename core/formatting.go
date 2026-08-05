package core

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
)

var (
	reUnfilledPlaceholders = regexp.MustCompile(`\${[^}]*}`)
	// ${--flag:opt}
	reColon = regexp.MustCompile(`\$\{([^:}]+):([^}]+)\}`)
	// ${--flag=opt}
	reEquals = regexp.MustCompile(`\$\{([^=}]+)=([^}]+)\}`)
)

// RunAllFormatters runs every configured formatter for uri in sequence, each
// one fed the previous one's output, and returns the edits that turn the
// document into the final result.
func (h *LangHandler) RunAllFormatters(
	ctx context.Context, reporter Reporter, uri types.DocumentURI, rng *types.Range,
	options types.FormattingOptions) ([]types.TextEdit, error) {
	snap, err := h.snapshot(uri)
	if err != nil {
		return nil, err
	}
	f := snap.file

	configs := snap.resolveConfigs(func(cfg types.Language) bool { return cfg.FormatCommand != "" })
	if len(configs) == 0 {
		logs.Log.Logf(logs.Warn, "no matching format configs for LanguageID: %v", f.LanguageID)
		return nil, nil
	}

	progressToken := types.NewProgressToken()
	reporter.Progress(ctx, types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressBegin("Formatting document", nil, nil),
	})
	// deferred so that an early return can never leave the client with a
	// progress token that is begun but never ended
	defer reporter.Progress(ctx, types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressEnd(nil),
	})

	originalText := f.Text
	formattedText := originalText
	formatted := false

	errors := make([]string, 0)
	for _, config := range configs {
		newText, err := formatDocument(ctx, config.rootPath, f.NormalizedFilename, formattedText, rng, options, config.Language)

		if err != nil {
			errors = append(errors, err.Error())
			logs.Log.Logln(logs.Error, err.Error())
			continue
		}

		formatted = true
		formattedText = newText
	}

	if !formatted {
		return nil, fmt.Errorf("could not format for LanguageID: %s. All errors: %v", f.LanguageID, errors)
	}

	// the edits below are a diff against the text the formatters started from, so
	// they only apply cleanly to a document that has not moved since. a client
	// that formats synchronously blocks input and cannot get here; one that
	// formats asynchronously can, and applying a stale diff there would corrupt
	// the document. a version comparison is cheap enough to do regardless.
	if err := h.ensureUnchanged(uri, f.Version); err != nil {
		return nil, err
	}

	logs.Log.Logln(logs.Info, "format succeeded")

	return ComputeEdits(uri, originalText, formattedText)
}

// this needs to accept textToFormat because in case we have multiple formatters, we can pass previous formatted text.
// otherwise, we'd format the original file over and over.
func formatDocument(ctx context.Context, rootPath string, filename string, textToFormat string, rng *types.Range, options types.FormattingOptions, config types.Language) (string, error) {
	cmdStr := buildFormatCommandString(rootPath, filename, textToFormat, options, rng, config.FormatCommand)
	cmd := buildExecCmd(ctx, cmdStr, rootPath, config.Env, strings.NewReader(textToFormat))
	out, err := runFormattingCommand(cmd)

	logs.Log.Logln(logs.Info, cmdStr)
	logs.Log.Logln(logs.Debug, out)

	if err != nil {
		return "", fmt.Errorf("formatting error: %s", err)
	}

	return strings.ReplaceAll(out, carriageReturn, ""), nil
}

func resolveOptionsPlaceholder(re *regexp.Regexp, match string, options map[string]any, sep string) string {
	parts := re.FindStringSubmatch(match)
	flag, opt := parts[1], parts[2]

	neg := strings.HasPrefix(opt, "!")
	key := strings.TrimPrefix(opt, "!")

	v, ok := options[key]
	if !ok {
		return match // no option found
	}

	switch b := v.(type) {
	case bool:
		if b == !neg { // bool true and not negated, or bool false and negated
			return flag
		}
		return "" // remove placeholder
	default:
		if neg {
			return "" // negated default makes no sense
		}
		return fmt.Sprintf("%s%s%v", flag, sep, v)
	}
}

func applyOptionsPlaceholders(command string, options map[string]any) string {
	// Handle : syntax (flag:value)
	command = reColon.ReplaceAllStringFunc(command, func(match string) string {
		return resolveOptionsPlaceholder(reColon, match, options, " ")
	})

	// Handle = syntax (flag=value)
	command = reEquals.ReplaceAllStringFunc(command, func(match string) string {
		return resolveOptionsPlaceholder(reEquals, match, options, "=")
	})

	return strings.TrimSpace(command)
}

func applyRangePlaceholders(command string, rng *types.Range, text string) string {
	lines := strings.Split(text, "\n")
	charStart := convertRowColToIndex(lines, rng.Start.Line, rng.Start.Character)
	charEnd := convertRowColToIndex(lines, rng.End.Line, rng.End.Character)

	rangeOptions := map[string]any{
		"charStart": charStart,
		"charEnd":   charEnd,
		"rowStart":  rng.Start.Line,
		"colStart":  rng.Start.Character,
		"rowEnd":    rng.End.Line,
		"colEnd":    rng.End.Character,
	}

	return applyOptionsPlaceholders(command, rangeOptions)
}

func buildFormatCommandString(rootPath string, filename string, textToFormat string, options types.FormattingOptions, rng *types.Range, command string) string {
	command = replaceMagicStrings(command, filename, rootPath)
	command = applyOptionsPlaceholders(command, options)

	if rng != nil {
		command = applyRangePlaceholders(command, rng, textToFormat)
	}

	// whatever is left is a placeholder the client gave no value for
	return reUnfilledPlaceholders.ReplaceAllString(command, "")
}

func runFormattingCommand(cmd *exec.Cmd) (string, error) {
	var buf bytes.Buffer
	cmd.Stderr = &buf
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %s", strings.Join(cmd.Args, " "), buf.String())
	}
	return string(b), nil
}

func convertRowColToIndex(lines []string, row, col int) int {
	row = max(row, 0)
	row = min(row, len(lines)-1)

	col = max(col, 0)
	col = min(col, len(lines[row]))

	index := 0
	for i := 0; i < row; i++ {
		// Add the length of each line plus 1 for the newline character
		index += len(lines[i]) + 1
	}
	index += col

	return index
}
