package types

import "time"

const Wildcard = "="

type Config struct {
	Languages      *map[string][]Language `json:"languages,omitempty"`
	LintDebounce   time.Duration          `json:"lintDebounce,omitempty"`
	FormatDebounce time.Duration          `json:"formatDebounce,omitempty"`
}

type Language struct {
	Env           []string `json:"env,omitempty"`
	RootMarkers   []string `json:"rootMarkers,omitempty"`
	RequireMarker bool     `json:"requireMarker,omitempty"`
	// prefix for lint message
	Prefix      string   `json:"prefix,omitempty"`
	LintFormats []string `json:"lintFormats,omitempty"`
	LintStdin   bool     `json:"lintStdin,omitempty"`
	// warning: this will be subtracted from the line reported by the linter
	LintOffset int `json:"lintOffset,omitempty"`
	// warning: this will be added to the column reported by the linter
	LintOffsetColumns  int                `json:"lintOffsetColumns,omitempty"`
	LintCommand        string             `json:"lintCommand,omitempty"`
	LintIgnoreExitCode bool               `json:"lintIgnoreExitCode,omitempty"`
	LintCategoryMap    map[string]string  `json:"lintCategoryMap,omitempty"`
	LintSource         string             `json:"lintSource,omitempty"`
	LintSeverity       DiagnosticSeverity `json:"lintSeverity,omitempty"`
	// defaults to true if not provided as a sanity default
	LintAfterOpen *bool `json:"lintAfterOpen,omitempty"`
	// defaults to true if not provided as a sanity default
	LintOnChange *bool `json:"lintOnChange,omitempty"`
	// defaults to true if not provided as a sanity default
	LintOnSave     *bool  `json:"lintOnSave,omitempty"`
	FormatCommand  string `json:"formatCommand,omitempty"`
	FormatCanRange bool   `json:"formatCanRange,omitempty"`
}

type EventType int

const (
	EventTypeChange EventType = iota
	EventTypeSave
	EventTypeOpen
)
