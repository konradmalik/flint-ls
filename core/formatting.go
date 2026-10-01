package core

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf16"

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
	snap, claim, err := h.claimFormatting(uri)
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
	// they only apply cleanly to a document that has not moved since, and only if
	// no newer request's edits get applied first. asked after the run rather than
	// before it, because a run that has already started is precisely the one a
	// newer request supersedes. a client that formats synchronously cannot get
	// here; one that formats asynchronously can, and applying a stale diff there
	// would corrupt the document.
	if err := h.ensureCurrent(uri, f.Version, claim); err != nil {
		return nil, err
	}

	logs.Log.Logln(logs.Info, "format succeeded")

	return ComputeEdits(originalText, formattedText)
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

// rangeValues are the values a ranged format fills option placeholders with.
func rangeValues(rng *types.Range, text string) map[string]any {
	lines := strings.Split(text, "\n")

	return map[string]any{
		"charStart": byteOffset(lines, rng.Start),
		"charEnd":   byteOffset(lines, rng.End),
		"rowStart":  rng.Start.Line,
		"colStart":  rng.Start.Character,
		"rowEnd":    rng.End.Line,
		"colEnd":    rng.End.Character,
	}
}

// buildFormatCommandString fills in every placeholder of command. The paths go
// in last: filled in first, a filename holding something that looks like a
// placeholder would be filled in as one.
func buildFormatCommandString(rootPath string, filename string, textToFormat string, options types.FormattingOptions, rng *types.Range, command string) string {
	values := make(map[string]any, len(options))
	maps.Copy(values, options)
	if rng != nil {
		maps.Copy(values, rangeValues(rng, textToFormat))
	}
	command = applyOptionsPlaceholders(command, values)

	// whatever is left is a placeholder the client gave no value for, apart from
	// the paths that have yet to go in
	command = reUnfilledPlaceholders.ReplaceAllStringFunc(command, func(placeholder string) string {
		if isPathPlaceholder(placeholder) {
			return placeholder
		}
		return ""
	})

	return replaceMagicStrings(command, filename, rootPath)
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

// byteOffset converts an lsp position into an offset into the text the lines
// were split from, counted in bytes. Positions outside the text are clamped to it.
//
// Bytes because the formatters that take an offset do not agree on its unit --
// stylua counts bytes, prettier utf16 units -- and bytes is what this always
// counted for every line before the one the position is on. efm-langserver
// counted the column on that line in utf16 units, which was right for no tool.
func byteOffset(lines []string, pos types.Position) int {
	row := min(max(pos.Line, 0), len(lines)-1)

	index := 0
	for _, line := range lines[:row] {
		// plus the newline the split removed
		index += len(line) + 1
	}

	// pos.Character counts utf16 units, which the line has to be walked to convert
	units := 0
	for i, r := range lines[row] {
		if units >= pos.Character {
			return index + i
		}
		units += utf16.RuneLen(r)
	}

	return index + len(lines[row])
}
