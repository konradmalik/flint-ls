package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// NewHandler returns a handler for the given language configuration. Passing nil
// is normal: most clients send their configuration in a didChangeConfiguration
// notification rather than at startup.
func NewHandler(configs map[string][]types.Language) *LangHandler {
	if configs == nil {
		configs = make(map[string][]types.Language)
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
	h.configs = config.Languages
}

func (h *LangHandler) CloseFile(uri types.DocumentURI) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.files, uri)
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

// resolvedConfig is a language config together with the working directory its
// tool will run in.
type resolvedConfig struct {
	types.Language
	rootPath string
}

// resolveConfigs picks the configs that apply to the snapshot's document --
// those registered for its language plus the wildcard ones -- and pairs each with
// its working directory. keep decides what "applies" means for the caller, which
// is the only thing linting and formatting disagree about here.
//
// Resolving the directory during selection rather than afterwards is what keeps
// the marker search to one walk per config: the same walk answers both whether a
// config requiring a marker may run at all and where its tool should run.
func (s documentSnapshot) resolveConfigs(keep func(types.Language) bool) []resolvedConfig {
	var configs []resolvedConfig
	for _, cfg := range slices.Concat(s.configs[s.file.LanguageID], s.configs[types.Wildcard]) {
		if !keep(cfg) {
			continue
		}

		dir := matchRootPath(s.file.NormalizedFilename, cfg.RootMarkers)
		if dir == "" {
			if cfg.RequireMarker {
				continue
			}
			dir = s.rootPath
		}

		configs = append(configs, resolvedConfig{Language: cfg, rootPath: dir})
	}

	return configs
}

// matchRootPath returns the closest ancestor directory of fname that holds one of
// the markers, or "" if there is none. A marker ending in "/" has to be a
// directory, anything else has to not be one.
func matchRootPath(fname string, markers []string) string {
	if len(markers) == 0 {
		// nothing to look for, so nothing to walk the tree for either
		return ""
	}

	dir := filepath.Dir(fname)
	for {
		if dirHasMarker(dir, markers) {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// reached the root without a match
			return ""
		}
		dir = parent
	}
}

// dirHasMarker reports whether dir contains one of the markers. filepath.Glob
// answers a literal marker -- which is what markers almost always are -- with a
// single stat, and only falls back to reading the whole directory for a marker
// that actually has a pattern in it.
func dirHasMarker(dir string, markers []string) bool {
	for _, marker := range markers {
		wantDir := strings.HasSuffix(marker, "/")

		matches, err := filepath.Glob(filepath.Join(dir, strings.TrimSuffix(marker, "/")))
		if err != nil {
			// malformed pattern: it cannot match anything, anywhere
			continue
		}

		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && info.IsDir() == wantDir {
				return true
			}
		}
	}

	return false
}

func isStdinPlaceholder(s string) bool {
	switch s {
	case "stdin", "-", "<text>", "<stdin>":
		return true
	default:
		return false
	}
}

// replaceMagicStrings fills in the placeholders a command may use.
//
// Commands run through a shell, so a value is escaped for wherever in the command
// it lands. A bare placeholder is quoted, because paths routinely contain spaces
// and parentheses and pasting one in bare would split it into several arguments.
// A placeholder the config already put inside quotes is not quoted again -- that
// would hand the tool a filename with quote characters in it -- but it is still
// escaped for the kind of quotes it is in, because a value can end that string
// early, and inside double quotes the shell would go on to expand a $(...) in a
// filename instead of passing it along.
//
// A path holding none of those characters comes out exactly as it did before, so
// configs that quote their own placeholders are unaffected.
//
// ${FILEEXT} is never quoted: it is substituted mid-word, as in foo.${FILEEXT}.
func replaceMagicStrings(command, fname, rootPath string) string {
	replacements := []struct {
		placeholder string
		value       string
		// whether a bare occurrence is quoted, which is what a path needs and an
		// extension spliced into the middle of a word must not have
		quoteBare bool
	}{
		{inputPlaceholder, fname, true},
		{filenamePlaceholder, filepath.FromSlash(fname), true},
		{rootPlaceholder, rootPath, true},
		{fileextPlaceholder, strings.TrimPrefix(filepath.Ext(fname), "."), false},
	}

	var out strings.Builder
	var inSingle, inDouble bool

scan:
	for i := 0; i < len(command); {
		for _, r := range replacements {
			if !strings.HasPrefix(command[i:], r.placeholder) {
				continue
			}

			switch {
			case inSingle:
				out.WriteString(escapeInSingleQuotes(r.value))
			case inDouble:
				out.WriteString(escapeInDoubleQuotes(r.value))
			case r.quoteBare:
				out.WriteString(shellQuote(r.value))
			default:
				out.WriteString(r.value)
			}
			i += len(r.placeholder)
			continue scan
		}

		// a quote of one kind inside the other kind is just a character
		switch c := command[i]; {
		case inSingle:
			inSingle = c != '\''
		case inDouble:
			inDouble = c != '"'
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		}

		out.WriteByte(command[i])
		i++
	}

	return out.String()
}
