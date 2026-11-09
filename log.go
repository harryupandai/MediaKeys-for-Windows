package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

var logFile *os.File

func initLog() {
	d := dataDir()
	if d == "" {
		return
	}
	os.MkdirAll(d, 0755)
	logFile, _ = os.OpenFile(filepath.Join(d, "MediaKeys.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	logf("MediaKeys started")
}

func logf(format string, a ...any) {
	if logFile == nil {
		return
	}
	fmt.Fprintf(logFile, "%s  %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
	logFile.Sync()
}

// guard keeps a bug in one handler from killing the whole program, and logs it.
func guard(where string) {
	if r := recover(); r != nil {
		logf("CRASH in %s: %v\n%s", where, r, debug.Stack())
	}
}
