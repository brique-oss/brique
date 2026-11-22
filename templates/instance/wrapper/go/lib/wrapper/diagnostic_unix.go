// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

//go:build !windows

package wrapper

import (
	"os"
	"os/signal"
	"syscall"
)

// installDump writes the state of the wrapper to a file when it receives SIGUSR1. The returned function stops listening.
func installDump(scheduler *WorkScheduler, log func(format string, args ...any)) (stop func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-signals:
				path, err := DumpState(scheduler, "")
				if err != nil {
					log("event=state_dump_failed path=%s error=%v", path, err)
				} else {
					log("event=state_dumped path=%s", path)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(signals); close(done) }
}
