package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/konradmalik/flint-ls/types"
)

// LangHandler owns the document store and the language configuration.
//
// Document sync notifications arrive on the connection's read loop while lint
// and format runs execute on their own goroutines, so every field below is
// guarded by mu. Long running work must never hold mu: it takes a snapshot
// first (see snapshot) and operates on that.
type LangHandler struct {
	mu       sync.RWMutex
	configs  map[string][]types.Language
	files    map[types.DocumentURI]*fileRef
	rootPath string
}

type fileRef struct {
	Version            int
	NormalizedFilename string
	LanguageID         string
	Text               string
	Uri                types.DocumentURI
}

// documentSnapshot is a consistent, read-only view of everything a lint or
// format run needs. It is copied out under LangHandler.mu so that concurrent
// didChange/didClose notifications cannot mutate it mid-run.
type documentSnapshot struct {
	file     fileRef
	configs  map[string][]types.Language
	rootPath string
}

// ErrDocumentChanged reports that a document was edited while it was being
// processed, which makes the result unusable: it describes a transformation of
// text the client has already replaced. Only reachable from a client that does
// not wait for the operation it asked for.
var ErrDocumentChanged = errors.New("document changed while being processed")

// ensureUnchanged reports whether uri still holds the version that was read at
// the start of an operation. A document that has been closed counts as changed:
// either way the result describes text the client no longer has, and the caller
// has nothing else to decide.
func (h *LangHandler) ensureUnchanged(uri types.DocumentURI, version int) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	f, ok := h.files[uri]
	if !ok {
		return fmt.Errorf("%w: %v was closed", ErrDocumentChanged, uri)
	}
	if f.Version != version {
		return fmt.Errorf("%w: %v moved from version %d to %d", ErrDocumentChanged, uri, version, f.Version)
	}

	return nil
}

// snapshot copies the state needed to process uri.
//
// The configs map is shared rather than cloned: UpdateConfiguration always
// replaces the map wholesale and nothing mutates one in place, so a reader that
// obtained the map under the lock can keep reading it without one.
func (h *LangHandler) snapshot(uri types.DocumentURI) (documentSnapshot, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	f, ok := h.files[uri]
	if !ok {
		return documentSnapshot{}, fmt.Errorf("document not found: %v", uri)
	}

	return documentSnapshot{file: *f, configs: h.configs, rootPath: h.rootPath}, nil
}

func NewConfig() *types.Config {
	languages := make(map[string][]types.Language)
	return &types.Config{
		Languages: &languages,
	}
}

func NewHandler(config *types.Config) *LangHandler {
	configs := make(map[string][]types.Language)
	if config.Languages != nil {
		configs = *config.Languages
	}

	return &LangHandler{
		configs: configs,
		files:   make(map[types.DocumentURI]*fileRef),
	}
}

func (h *LangHandler) Initialize(params types.InitializeParams) (types.InitializeResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if params.RootURI != "" {
		rootPath, err := PathFromURI(params.RootURI)
		if err != nil {
			return types.InitializeResult{}, err
		}
		h.rootPath = filepath.Clean(rootPath)
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
	if config.Languages == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.configs = *config.Languages
}

func (h *LangHandler) CloseFile(uri types.DocumentURI) error {
	h.mu.Lock()
	defer h.mu.Unlock()

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

	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[uri] = f

	return nil
}

func (h *LangHandler) UpdateFile(uri types.DocumentURI, text string, version *int) error {
	h.mu.Lock()
	defer h.mu.Unlock()

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

// findRootPath resolves the working directory for a tool invocation: the
// closest ancestor directory matching one of the root markers, else fallback.
func findRootPath(fname string, markers []string, fallback string) string {
	if dir := matchRootPath(fname, markers); dir != "" {
		return dir
	}

	return fallback
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
