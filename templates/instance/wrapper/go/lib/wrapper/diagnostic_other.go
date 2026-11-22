// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

//go:build windows

package wrapper

func installDump(*WorkScheduler, func(string, ...any)) (stop func()) { return func() {} }
