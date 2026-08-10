package core

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konradmalik/flint-ls/types"
)

// The corpus is cloned by `make test-efmls` rather than checked in, so that what CI
// tests against is what upstream ships today instead of a copy that went stale in
// git. Without one these tests skip, which is what a nix build and an aeroplane both
// need; CI sets EFMLS_REQUIRE_CORPUS so that there a skip is a failure instead.
//
// EFMLS_CONFIGS_DIR points at a corpus somewhere else -- a nix store path, or a
// checkout being bisected.
const efmlsCloneDir = "testdata/efmls-configs"

// config keys that we explicitly do not support
var efmlsIgnoredKeys = map[string]string{
	"formatStdin":  "flint-ls always feeds the formatter on stdin, which is what efmlsFileFormatters is about",
	"lintDebounce": "flint-ls debounces every document by one server-wide interval instead of per language",
	"lintFormat":   "a typo for lintFormats upstream, so efm-langserver ignores it too",
}

// we don't accept non-stdin, so ignore those formatters
var efmlsFileFormatters = []string{
	"formatters/buf",
	"formatters/clang_tidy",
	"formatters/dotnet_format",
	"formatters/eslint",
	"formatters/php_cs_fixer",
	"formatters/pint",
	"formatters/protolint",
	"formatters/sqlfluff",
	"formatters/uncrustify",
	"formatters/yq",
}

// deliberately hostile strings
const (
	efmlsRoot = "/tmp/it's a \"project\" (v2)"
	efmlsFile = efmlsRoot + "/a dir/$(echo hi) `x` & 'y'/файл.go"
	efmlsText = "first\nsecond\n"
)

func TestEfmlsConfigsAreSupported(t *testing.T) {
	configs := loadEfmlsConfigs(t)

	for _, name := range slices.Sorted(maps.Keys(configs)) {
		t.Run(name, func(t *testing.T) {
			lang := decodeEfmlsConfig(t, configs[name])

			if len(lang.LintFormats) > 0 {
				_, err := buildErrorformats(lang.LintFormats)
				assert.NoError(t, err, "flint-ls cannot parse this linter's output at all")
			}

			if lang.LintCommand != "" {
				f := fileRef{NormalizedFilename: efmlsFile, LanguageID: "test", Text: efmlsText}
				built := buildLintCommandString(efmlsRoot, f, lang)
				checkBuiltCommand(t, "lintCommand", lang.LintCommand, built)

				if !lang.LintStdin {
					// a linter that is not handed the document has to be told which file
					// to read, whether or not the config remembered to ask for it
					assert.Contains(t, built, "файл.go",
						"lintCommand: a linter that does not read stdin was given no file to read")
				}
			}

			if lang.FormatCommand != "" {
				checkFormatCommand(t, lang)
			}
		})
	}
}

// TestEfmlsFileFormattersAreStillTheKnownOnes fails when upstream adds a formatter
// that wants a file rather than stdin, so the gap gets looked at while it is still
// a list in a test.
func TestEfmlsFileFormattersAreStillTheKnownOnes(t *testing.T) {
	configs := loadEfmlsConfigs(t)

	var fileBased []string
	for name, raw := range configs {
		var cfg struct {
			FormatCommand string `json:"formatCommand"`
			FormatStdin   *bool  `json:"formatStdin"`
		}
		require.NoError(t, json.Unmarshal(raw, &cfg))

		// an absent formatStdin means false to efm-langserver, so yq counts too
		if cfg.FormatCommand != "" && !boolOrDefault(cfg.FormatStdin, false) {
			fileBased = append(fileBased, name)
		}
	}
	slices.Sort(fileBased)

	assert.Equal(t, efmlsFileFormatters, fileBased,
		"the set of formatters flint-ls cannot run correctly has changed")
}

// decodeEfmlsConfig checks that flint-ls recognises every key a config sets and
// can hold every value, and returns what it decoded.
func decodeEfmlsConfig(t *testing.T, raw json.RawMessage) types.Language {
	t.Helper()

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &keys))

	known := languageSettingKeys()
	supported := make(map[string]json.RawMessage, len(keys))
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if known[key] {
			supported[key] = keys[key]
			continue
		}
		_, ignored := efmlsIgnoredKeys[key]
		assert.True(t, ignored, "types.Language has no %q, so flint-ls silently drops it", key)
	}

	// the recognised keys are decoded on their own so that a value flint-ls cannot
	// hold is reported as the type error it is, instead of as an unknown key
	body, err := json.Marshal(supported)
	require.NoError(t, err)

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()

	var lang types.Language
	require.NoError(t, dec.Decode(&lang), "flint-ls cannot hold the values this config sets")

	return lang
}

// languageSettingKeys is the set of settings keys types.Language understands,
// taken from the struct itself so that a field added there needs no change here.
func languageSettingKeys() map[string]bool {
	lang := reflect.TypeFor[types.Language]()

	keys := make(map[string]bool, lang.NumField())
	for i := range lang.NumField() {
		if name, _, _ := strings.Cut(lang.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}

	return keys
}

func checkFormatCommand(t *testing.T, lang types.Language) {
	t.Helper()

	options := types.FormattingOptions{"tabSize": 4, "insertSpaces": true}
	checkBuiltCommand(t, "formatCommand", lang.FormatCommand,
		buildFormatCommandString(efmlsRoot, efmlsFile, efmlsText, options, nil, lang.FormatCommand))

	if !lang.FormatCanRange {
		return
	}

	// a range formatter is also asked to format a range, because that is the path
	// that fills the ${--flag:charStart} placeholders in
	rng := &types.Range{
		Start: types.Position{Line: 0, Character: 2},
		End:   types.Position{Line: 1, Character: 3},
	}
	checkBuiltCommand(t, "formatCommand over a range", lang.FormatCommand,
		buildFormatCommandString(efmlsRoot, efmlsFile, efmlsText, options, rng, lang.FormatCommand))
}

// leftoverPlaceholder matches an efm placeholder that survived substitution. One
// that reaches the shell as a literal makes the tool look for a file named
// "${INPUT}", so the run fails with something that reads like the tool's fault.
var leftoverPlaceholder = regexp.MustCompile(`\$\{[A-Za-z-][^}]*\}`)

// checkBuiltCommand checks that a command flint-ls is about to hand to a shell is
// one the shell can make sense of, and that the values the config asked for are in
// it.
func checkBuiltCommand(t *testing.T, what, configured, built string) {
	t.Helper()

	assert.NotRegexp(t, leftoverPlaceholder, built, "%s: a placeholder was left unfilled", what)

	if strings.Contains(configured, inputPlaceholder) || strings.Contains(configured, filenamePlaceholder) {
		assert.Contains(t, built, "файл.go", "%s: the filename never made it into the command", what)
	}
	if strings.Contains(configured, rootPlaceholder) {
		assert.Contains(t, built, "(v2)", "%s: the root path never made it into the command", what)
	}
	if strings.Contains(configured, fileextPlaceholder) {
		assert.Contains(t, built, "go", "%s: the file extension never made it into the command", what)
	}

	assertShellCanParse(t, what, built)
}

// assertShellCanParse runs the command through the shell's syntax-only mode, which
// parses without running any of it. Quoting the escaping got wrong surfaces here,
// usually as an unterminated string -- which is the failure a path with a quote in
// it used to cause at the point of running the tool.
func assertShellCanParse(t *testing.T, what, command string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		// cmd.exe has no syntax-only mode, and these configs are written for sh
		return
	}

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(command)

	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "%s: the shell cannot parse this:\n\t%s\n%s", what, command, out)
}

// loadEfmlsConfigs reads every config out of the corpus by evaluating the lua it is
// written in.
func loadEfmlsConfigs(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	dir := efmlsCorpusDir(t)

	if _, err := exec.LookPath("nvim"); err != nil {
		skipUnlessRequired(t, "the corpus is lua and needs nvim to evaluate: %v", err)
	}

	// which commit was checked, so that a failure here can be tied to an upstream
	// change without having to guess what the corpus looked like at the time
	if sha, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output(); err == nil {
		t.Logf("efmls-configs %s at %s", dir, strings.TrimSpace(string(sha)))
	}

	// -u NONE and --noplugin because a machine with efmls-configs installed for its
	// own use loads it during startup, and require would then hand the script that
	// copy instead of the downloaded one
	cmd := exec.Command("nvim", "--headless", "-u", "NONE", "-i", "NONE", "--noplugin",
		"-l", filepath.Join("testdata", "efmls-extract.lua"), dir)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	require.NoError(t, err, "reading the configs out of %s: %s", dir, stderr.String())

	var configs map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &configs))
	require.Greater(t, len(configs), 100, "only %d configs in %s, which cannot be all of them",
		len(configs), dir)

	return configs
}

// efmlsCorpusDir returns the directory holding the efmls-configs lua tree.
func efmlsCorpusDir(t *testing.T) string {
	t.Helper()

	dir := cmp.Or(os.Getenv("EFMLS_CONFIGS_DIR"), efmlsCloneDir)
	if _, err := os.Stat(filepath.Join(dir, "lua")); err != nil {
		skipUnlessRequired(t, "no corpus in %s, run `make test-efmls`: %v", dir, err)
	}

	return dir
}

// skipUnlessRequired keeps this test out of the way where the corpus cannot be had: the nix sandbox.
// CI sets EFMLS_REQUIRE_CORPUS so that there a missing corpus is a failure
func skipUnlessRequired(t *testing.T, format string, args ...any) {
	t.Helper()

	if os.Getenv("EFMLS_REQUIRE_CORPUS") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}
