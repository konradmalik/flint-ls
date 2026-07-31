package logs

import (
	"log"
	"os"
)

// Log is the process-wide logger. It starts silent, so that anything logged
// before InitializeLogger runs cannot reach the client's stdio.
var Log = &Logger{createLogger(""), None}

type LogLevel int

const (
	None LogLevel = iota - 1
	Error
	Warn
	Info
	Debug
)

// Logger writes the messages that are at or below the level it was given.
type Logger struct {
	log   *log.Logger
	level LogLevel
}

func InitializeLogger(logfile string, level LogLevel) {
	Log = &Logger{createLogger(logfile), level}
}

// Enabled reports whether a message at level would be written. Callers use it to
// skip building a message that would only be thrown away.
func (logger *Logger) Enabled(level LogLevel) bool {
	return level <= logger.level
}

func (logger *Logger) Logln(level LogLevel, msg string) {
	if logger.Enabled(level) {
		logger.log.Println(msg)
	}
}

func (logger *Logger) Logf(level LogLevel, format string, v ...any) {
	if logger.Enabled(level) {
		logger.log.Printf(format, v...)
	}
}

// Printf makes this a jsonrpc2.Logger. That logs the entire message traffic, so
// it belongs at the debug level and nowhere else.
func (logger *Logger) Printf(format string, v ...any) {
	logger.Logf(Debug, format, v...)
}

func createLogger(logfile string) *log.Logger {
	if logfile == "" {
		return log.New(os.Stderr, "", log.LstdFlags)
	}

	f, err := os.OpenFile(logfile, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o660)
	if err != nil {
		log.Fatal(err)
	}
	return log.New(f, "", log.LstdFlags)
}
