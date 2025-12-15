package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/konradmalik/flint-ls/types"
)

const (
	inputPlaceholder    = "${INPUT}"
	fileextPlaceholder  = "${FILEEXT}"
	filenamePlaceholder = "${FILENAME}"
	rootPlaceholder     = "${ROOT}"
	carriageReturn      = "\r"
)

func normalizedFilenameFromUri(uri types.DocumentURI) (string, error) {
	fname, err := PathFromURI(uri)
	if err != nil {
		return "", fmt.Errorf("invalid uri: %v: %v", err, uri)
	}
	fname = filepath.ToSlash(fname)
	return fname, nil
}

func getAllConfigsForLang(allConfigs map[string][]types.Language, langId string) []types.Language {
	configsForLang := make([]types.Language, 0)
	if cfgs, ok := allConfigs[langId]; ok {
		configsForLang = append(configsForLang, cfgs...)
	}
	if cfgs, ok := allConfigs[types.Wildcard]; ok {
		configsForLang = append(configsForLang, cfgs...)
	}
	return configsForLang
}

func buildExecCmd(ctx context.Context, command, rootPath string, textToFormat string, config types.Language, stdin bool) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell, shellFlag, command)
	cmd.Dir = rootPath
	cmd.Env = append(os.Environ(), config.Env...)
	if stdin {
		cmd.Stdin = strings.NewReader(textToFormat)
	}

	return cmd
}

func itoaPtrIfNotZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func boolOrDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func boolPtr(v bool) *bool { return &v }

func blackHoleProgress() chan types.ProgressParams {
	ch := make(chan types.ProgressParams)
	go func() {
		for range ch {
			// discard values
		}
	}()
	return ch
}
