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

func (h *LangHandler) RunAllFormatters(
	ctx context.Context, uri types.DocumentURI, rng *types.Range, options types.FormattingOptions,
	progress chan<- types.ProgressParams) ([]types.TextEdit, error) {
	f, ok := h.files[uri]
	if !ok {
		return nil, fmt.Errorf("document not found: %v", uri)
	}

	configs, err := getFormatConfigsForDocument(f.NormalizedFilename, f.LanguageID, h.configs)
	if err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		logs.Log.Logf(logs.Warn, "no matching format configs for LanguageID: %v", f.LanguageID)
		return nil, nil
	}

	progressToken := types.NewProgressToken()
	progress <- types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressBegin("Formatting document", nil, nil),
	}

	originalText := f.Text
	formattedText := originalText
	formatted := false

	errors := make([]string, 0)
	for _, config := range configs {
		rootPath := h.findRootPath(f.NormalizedFilename, config)
		newText, err := formatDocument(ctx, rootPath, f.NormalizedFilename, formattedText, rng, options, config)

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

	logs.Log.Logln(logs.Info, "format succeeded")

	progress <- types.ProgressParams{
		Token: progressToken,
		Value: types.NewWorkDoneProgressEnd(nil),
	}

	return ComputeEdits(uri, originalText, formattedText)
}

// this needs to accept textToFormat because in case we have multiple formatters, we can pass previous formatted text.
// otherwise, we'd format the original file over and over.
func formatDocument(ctx context.Context, rootPath string, filename string, textToFormat string, rng *types.Range, options types.FormattingOptions, config types.Language) (string, error) {
	cmdStr, err := buildFormatCommandString(rootPath, filename, textToFormat, options, rng, config.FormatCommand)
	if err != nil {
		return "", fmt.Errorf("command build error: %s", err)
	}

	cmd := buildExecCmd(ctx, cmdStr, rootPath, textToFormat, config, true)
	out, err := runFormattingCommand(cmd)

	logs.Log.Logln(logs.Info, cmdStr)
	logs.Log.Logln(logs.Debug, out)

	if err != nil {
		return "", fmt.Errorf("formatting error: %s", err)
	}

	return strings.ReplaceAll(out, carriageReturn, ""), nil
}

func resolveOptionsPlaceholder[T any](re *regexp.Regexp, match string, options map[string]T, sep string) string {
	parts := re.FindStringSubmatch(match)
	flag, opt := parts[1], parts[2]

	neg := strings.HasPrefix(opt, "!")
	key := strings.TrimPrefix(opt, "!")

	v, ok := options[key]
	if !ok {
		return match // no option found
	}

	switch b := any(v).(type) {
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

func applyOptionsPlaceholders[T any](command string, options map[string]T) (string, error) {
	// Handle : syntax (flag:value)
	command = reColon.ReplaceAllStringFunc(command, func(match string) string {
		return resolveOptionsPlaceholder(reColon, match, options, " ")
	})

	// Handle = syntax (flag=value)
	command = reEquals.ReplaceAllStringFunc(command, func(match string) string {
		return resolveOptionsPlaceholder(reEquals, match, options, "=")
	})

	return strings.TrimSpace(command), nil
}

func applyRangePlaceholders(command string, rng *types.Range, text string) (string, error) {
	lines := strings.Split(text, "\n")
	charStart := convertRowColToIndex(lines, rng.Start.Line, rng.Start.Character)
	charEnd := convertRowColToIndex(lines, rng.End.Line, rng.End.Character)

	rangeOptions := map[string]int{
		"charStart": charStart,
		"charEnd":   charEnd,
		"rowStart":  rng.Start.Line,
		"colStart":  rng.Start.Character,
		"rowEnd":    rng.End.Line,
		"colEnd":    rng.End.Character,
	}

	return applyOptionsPlaceholders(command, rangeOptions)
}

func buildFormatCommandString(rootPath string, filename string, textToFormat string, options types.FormattingOptions, rng *types.Range, command string) (string, error) {
	command = replaceMagicStrings(command, filename, rootPath)

	var err error
	command, err = applyOptionsPlaceholders(command, options)
	if err != nil {
		return "", err
	}

	if rng != nil {
		command, err = applyRangePlaceholders(command, rng, textToFormat)
		if err != nil {
			return "", err
		}
	}

	return reUnfilledPlaceholders.ReplaceAllString(command, ""), nil
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

func getFormatConfigsForDocument(fname, langId string, allConfigs map[string][]types.Language) ([]types.Language, error) {
	var configs []types.Language
	for _, cfg := range getAllConfigsForLang(allConfigs, langId) {
		if cfg.FormatCommand == "" {
			continue
		}
		if dir := matchRootPath(fname, cfg.RootMarkers); dir == "" && cfg.RequireMarker {
			continue
		}

		configs = append(configs, cfg)
	}

	return configs, nil
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
