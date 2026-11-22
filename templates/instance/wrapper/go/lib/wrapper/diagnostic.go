// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"
)

// A diagnostic on demand for a wrapper that stops answering. A frozen wrapper (its CPU at zero, calls waiting for it) says nothing about
// what it waits for. On the signal SIGUSR1 this writes, to a file, the state of the execution slots and every goroutine with its stack:
//
//	kill -USR1 <pid of the wrapper>           the file is $TMPDIR/brique_wrapper_tasks_<pid>.txt
//
// It only reads: it changes nothing, and does nothing until the signal comes.

// DumpState writes the state of the scheduler and every goroutine to path ("" for the default file) and returns the path.
func DumpState(scheduler *WorkScheduler, path string) (string, error) {
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("brique_wrapper_tasks_%d.txt", os.Getpid()))
	}
	out, err := os.Create(path)
	if err != nil {
		return path, err
	}
	defer out.Close()
	fmt.Fprintf(out, "pid %d at %s\n", os.Getpid(), time.Now().Format("15:04:05"))
	if scheduler == nil {
		fmt.Fprintln(out, "scheduler: none")
	} else {
		stats := scheduler.Stats()
		fmt.Fprintf(out, "scheduler: slots free %d of %d, active %d, pending %d, queued %d of %d, refused %d, locks %d, ordered chains %d\n",
			stats.FreeSlots, stats.MaxConcurrency, stats.Active, stats.Pending, stats.Queued, stats.QueueLimit, stats.Refused, stats.Locks, stats.OrderedChains)
	}
	fmt.Fprintf(out, "settings not at their default: %v\n", SettingsNotDefault())
	fmt.Fprintln(out)
	return path, pprof.Lookup("goroutine").WriteTo(out, 2)
}
