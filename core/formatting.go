package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/types"
)

// rePlaceholder matches any ${...} placeholder.
var rePlaceholder = regexp.MustCompile(`\$\{[^}]*\}`)

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

	// a formatter that cannot format a range would format the whole document
	// when asked for a few lines of it
	configs := snap.resolveConfigs(func(cfg types.Language) bool {
		return cfg.FormatCommand != "" && (rng == nil || cfg.FormatCanRange)
	})
	if len(configs) == 0 {
		logs.Log.Logf(logs.Warn, "no matching format configs for LanguageID: %v (range: %t)", f.LanguageID, rng != nil)
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

	// a formatter that fails is skipped, and the rest carry on from the text it
	// was given; only when every one of them fails is there nothing to return
	formattedText := f.Text
	var failures []error
	for _, config := range configs {
		newText, err := formatDocument(ctx, config.rootPath, f.NormalizedFilename, formattedText, rng, options, config.Language)
		if err != nil {
			logs.Log.Logln(logs.Error, err.Error())
			failures = append(failures, err)
			continue
		}
		formattedText = newText
	}

	if len(failures) == len(configs) {
		return nil, fmt.Errorf("could not format for LanguageID: %s: %w", f.LanguageID, errors.Join(failures...))
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

	return ComputeEdits(f.Text, formattedText)
}

// formatDocument runs one formatter on textToFormat, which is the output of the
// formatter before it rather than the document, so that formatters stack.
func formatDocument(ctx context.Context, rootPath string, filename string, textToFormat string, rng *types.Range, options types.FormattingOptions, config types.Language) (string, error) {
	cmdStr := buildFormatCommandString(rootPath, filename, textToFormat, options, rng, config.FormatCommand)
	cmd := buildExecCmd(ctx, cmdStr, rootPath, config.Env, strings.NewReader(textToFormat))

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	logs.Log.Logln(logs.Info, cmdStr)
	logs.Log.Logln(logs.Debug, string(out))

	if err != nil {
		// the exit status says how it failed, stderr what the tool had to say
		return "", fmt.Errorf("formatting error: %s: %w: %s", cmdStr, err, strings.TrimSpace(stderr.String()))
	}

	return strings.ReplaceAll(string(out), carriageReturn, ""), nil
}

// applyOptionsPlaceholders fills in the ${flag:option} and ${flag=option}
// placeholders of command from values, and drops every other placeholder except
// the paths, which replaceMagicStrings fills in afterwards.
func applyOptionsPlaceholders(command string, values map[string]any) string {
	command = rePlaceholder.ReplaceAllStringFunc(command, func(placeholder string) string {
		if isPathPlaceholder(placeholder) {
			return placeholder
		}
		return optionArgument(placeholder[len("${"):len(placeholder)-len("}")], values)
	})

	return strings.TrimSpace(command)
}

// optionArgument renders the body of an option placeholder: flag:option becomes
// "flag value", flag=option becomes "flag=value", and a bool option becomes the
// bare flag when true -- or when false, for an option negated as !option. An
// option without a value, or that is not one, renders as nothing.
func optionArgument(body string, values map[string]any) string {
	i := strings.IndexAny(body, ":=")
	if i <= 0 {
		return ""
	}
	flag, opt := body[:i], body[i+1:]
	sep := " "
	if body[i] == '=' {
		sep = "="
	}

	negated := strings.HasPrefix(opt, "!")
	v, ok := values[strings.TrimPrefix(opt, "!")]
	if !ok {
		return ""
	}

	switch v := v.(type) {
	case bool:
		if v != negated {
			return flag
		}
		return ""
	default:
		if negated {
			// negating a value that is not true or false means nothing
			return ""
		}
		return fmt.Sprintf("%s%s%v", flag, sep, v)
	}
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

	return replaceMagicStrings(command, filename, rootPath)
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
