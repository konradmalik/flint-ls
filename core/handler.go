package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/konradmalik/flint-ls/types"
)

type LangHandler struct {
	configs  map[string][]types.Language
	files    map[types.DocumentURI]*fileRef
	RootPath string
}

type fileRef struct {
	Version            int
	NormalizedFilename string
	LanguageID         string
	Text               string
	Uri                types.DocumentURI
}

func NewConfig() *types.Config {
	languages := make(map[string][]types.Language)
	return &types.Config{
		Languages: &languages,
	}
}

func NewHandler(config *types.Config) *LangHandler {
	handler := &LangHandler{
		configs: *config.Languages,
		files:   make(map[types.DocumentURI]*fileRef),
	}
	return handler
}

func (h *LangHandler) Initialize(params types.InitializeParams) (types.InitializeResult, error) {
	if params.RootURI != "" {
		rootPath, err := PathFromURI(params.RootURI)
		if err != nil {
			return types.InitializeResult{}, err
		}
		h.RootPath = filepath.Clean(rootPath)
	}

	var hasFormatCommand bool
	var hasRangeFormatCommand bool

	if params.InitializationOptions != nil {
		hasFormatCommand = params.InitializationOptions.DocumentFormatting
		hasRangeFormatCommand = params.InitializationOptions.RangeFormatting
	}

	for _, config := range h.configs {
		for _, lang := range config {
			if lang.FormatCommand != "" {
				hasFormatCommand = true
				if lang.FormatCanRange {
					hasRangeFormatCommand = true
					break
				}
			}
		}
	}

	return types.InitializeResult{
		Capabilities: types.ServerCapabilities{
			PositionEncoding: types.UTF16,
			TextDocumentSync: types.TextDocumentSyncOptions{
				OpenClose: true,
				Change:    types.TDSKFull,
			},
			DocumentFormattingProvider: hasFormatCommand,
			RangeFormattingProvider:    hasRangeFormatCommand,
		},
	}, nil
}

func (h *LangHandler) UpdateConfiguration(config *types.Config) {
	if config.Languages != nil {
		h.configs = *config.Languages
	}
}

func (h *LangHandler) CloseFile(uri types.DocumentURI) error {
	delete(h.files, uri)
	return nil
}

func (h *LangHandler) OpenFile(uri types.DocumentURI, languageID string, version int, text string) error {
	fname, err := normalizedFilenameFromUri(uri)
	if err != nil {
		return err
	}

	f := &fileRef{
		Text:               text,
		LanguageID:         languageID,
		Version:            version,
		NormalizedFilename: fname,
		Uri:                uri,
	}
	h.files[uri] = f

	return nil
}

func (h *LangHandler) UpdateFile(uri types.DocumentURI, text string, version *int) error {
	f, ok := h.files[uri]
	if !ok {
		return fmt.Errorf("document not found: %v", uri)
	}
	f.Text = text
	if version != nil {
		f.Version = *version
	}

	return nil
}

func (h *LangHandler) findRootPath(fname string, lang types.Language) string {
	if dir := matchRootPath(fname, lang.RootMarkers); dir != "" {
		return dir
	}

	return h.RootPath
}

func matchRootPath(fname string, markers []string) string {
	dir := filepath.Dir(fname)
	var prev string
	for dir != prev {
		files, _ := os.ReadDir(dir)
		for _, file := range files {
			name := file.Name()
			isDir := file.IsDir()
			for _, marker := range markers {
				if strings.HasSuffix(marker, "/") {
					if !isDir {
						continue
					}
					marker = strings.TrimRight(marker, "/")
					if ok, _ := filepath.Match(marker, name); ok {
						return dir
					}
				} else {
					if isDir {
						continue
					}
					if ok, _ := filepath.Match(marker, name); ok {
						return dir
					}
				}
			}
		}
		prev = dir
		dir = filepath.Dir(dir)
	}

	return ""
}

func isStdinPlaceholder(s string) bool {
	switch s {
	case "stdin", "-", "<text>", "<stdin>":
		return true
	default:
		return false
	}
}

func replaceMagicStrings(command, fname, rootPath string) string {
	ext := filepath.Ext(fname)
	ext = strings.TrimPrefix(ext, ".")

	command = strings.ReplaceAll(command, inputPlaceholder, escapeBrackets(fname))
	command = strings.ReplaceAll(command, fileextPlaceholder, ext)
	command = strings.ReplaceAll(command, filenamePlaceholder, escapeBrackets(filepath.FromSlash(fname)))
	command = strings.ReplaceAll(command, rootPlaceholder, escapeBrackets(rootPath))

	return command
}

func escapeBrackets(path string) string {
	path = strings.ReplaceAll(path, "(", `\(`)
	path = strings.ReplaceAll(path, ")", `\)`)

	return path
}
