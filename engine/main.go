/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	context "brique_engine/context"
	"brique_engine/junction"
	"brique_engine/shared"
)

func main() {
	rootPathFlag := flag.String("root_path", "", "Path to Brique instance root directory (contains instance root context.json)")
	flag.Parse()

	rootDir := ""
	if *rootPathFlag != "" {
		rootDir = *rootPathFlag
	}
	if rootDir == "" {
		fmt.Fprintf(os.Stderr, "Brique failed to start invalid empty root dir\n")
		os.Exit(1)
	}
	if abs, err := filepath.Abs(rootDir); err == nil {
		rootDir = abs
	} else {
		fmt.Fprintf(os.Stderr, "Brique failed to start invalid root dir %s\n", rootDir)
		os.Exit(1)
	}

	rootID := shared.RootContextID

	ctxCommReg := junction.NewContextCommRegistry()

	root, err := context.NewContextLoop(rootDir, rootID, ctxCommReg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique root context failed to initialize: %s\n", err.Error())
		os.Exit(1)
	}
	if root == nil || root.State() == shared.ContextFailed {
		fmt.Fprintln(os.Stderr, "Brique root context failed to initialize")
		os.Exit(1)
	}

	fmt.Printf("Brique Engine Started v%s. rootDir=%s rootId=%s\n", shared.EngineBinaryVersion, rootDir, rootID)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	sig := <-sigCh
	fmt.Printf("Stopping... (signal=%v)\n", sig)

	go func() {
		<-sigCh
		fmt.Println("Force exit.")
		os.Exit(1)
	}()

	root.Stop()
	fmt.Println("Stopped.")
}
