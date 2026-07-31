package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/konradmalik/flint-ls/core"
	"github.com/konradmalik/flint-ls/logs"
	"github.com/konradmalik/flint-ls/lsp"
)

const (
	name    = "flint-ls"
	version = "0.0.54"
)

var revision = "HEAD"

func main() {
	var logfile string
	var loglevel int
	var showVersion bool
	var usage bool

	flag.StringVar(&logfile, "logfile", "", "File to save logs into. If provided stderr won't be used anymore.")
	flag.IntVar(&loglevel, "loglevel", 2, "Set the log level. Max is 3 (debug), min is 0 (error). Higher number logs less. Set <0 for no logs.")
	flag.BoolVar(&showVersion, "v", false, "Print the version")
	flag.BoolVar(&usage, "h", false, "Show help")
	flag.Parse()

	if showVersion {
		fmt.Printf("%s %s (rev: %s/%s)\n", name, version, revision, runtime.Version())
		return
	}

	if usage || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(1)
	}

	logs.InitializeLogger(logfile, logs.LogLevel(min(max(loglevel, int(logs.None)), int(logs.Debug))))
	logs.Log.Logln(logs.Info, "reading on stdin, writing on stdout")

	// the languages arrive later, in a didChangeConfiguration notification
	handler := lsp.NewHandler(core.NewHandler(nil))

	<-jsonrpc2.NewConn(
		context.Background(),
		jsonrpc2.NewBufferedStream(stdrwc{}, jsonrpc2.VSCodeObjectCodec{}),
		lsp.OffloadSlowRequests(jsonrpc2.HandlerWithError(handler.Handle)),
		jsonrpc2.LogMessages(logs.Log)).DisconnectNotify()

	logs.Log.Logln(logs.Info, "flint-ls: connections closed")

	// the client is gone: abandon anything still scheduled instead of letting
	// pending linters run against a dead connection
	handler.Close()
	os.Exit(handler.ExitCode())
}

type stdrwc struct{}

func (stdrwc) Read(p []byte) (int, error) {
	return os.Stdin.Read(p)
}

func (c stdrwc) Write(p []byte) (int, error) {
	return os.Stdout.Write(p)
}

func (c stdrwc) Close() error {
	if err := os.Stdin.Close(); err != nil {
		return err
	}
	return os.Stdout.Close()
}
