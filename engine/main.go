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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	context "brique_engine/context"
	"brique_engine/junction"
	securitycatalog "brique_engine/security/catalog"
	"brique_engine/security/hostdir"
	securitymanager "brique_engine/security/manager"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

type securityReloader interface {
	Reload(trust.Catalog) error
}

type securityCatalogBuilder func(string) (trust.Catalog, error)

func runSecurityReloadWorker(rootDir string, reloader securityReloader, build securityCatalogBuilder, requests <-chan struct{}, results chan<- error, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-requests:
			closedWorld, err := build(rootDir)
			if err == nil {
				err = reloader.Reload(closedWorld)
			}
			select {
			case results <- err:
			case <-stop:
				return
			}
		}
	}
}

func main() {
	rootPathFlag := flag.String("root_path", "", "Path to Brique instance root directory (contains instance root context.json)")
	securityDirectoryFlag := flag.String("security_dir", "", "Host-local protected security directory (optional: an instance with a security/ folder finds ~/.brique/<instance> by itself)")
	securityGenerationFlag := flag.String("security_generation", "", "Initial protected security generation (required only for first activation)")
	auditMaxFileMiBFlag := flag.Int("audit_max_file_mib", 0, "Maximum size in MiB of one denial-journal file (default 64)")
	auditRetentionDaysFlag := flag.Int("audit_retention_days", 0, "Days of denial journal kept (default 14)")
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
	var securityManager *securitymanager.Manager
	locations, err := hostdir.Resolve(rootDir, *securityDirectoryFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start: %s\n", err)
		os.Exit(1)
	}
	ctxCommReg.SetMachineDirectory(locations.Machine)
	if locations.Machine == "" && *securityGenerationFlag != "" {
		fmt.Fprintln(os.Stderr, "Brique failed to start: security_generation requires a security directory")
		os.Exit(1)
	}
	if locations.Machine != "" {
		if info, statErr := os.Stat(locations.Machine); statErr != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "Brique failed to start: the security directory %s does not exist; create it with brique-sec init\n", locations.Machine)
			os.Exit(1)
		}
		closedWorld, err := securitycatalog.Build(rootDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Brique security catalog failed to initialize: %s\n", err)
			os.Exit(1)
		}
		securityManager, err = securitymanager.Start(securitymanager.Options{
			SecurityDirectory: locations.Machine, PolicyDirectory: locations.Policy, InitialGeneration: *securityGenerationFlag,
			RequireReadOnlyKeyFiles: true,
			AuditMaxFileMiB:         *auditMaxFileMiBFlag, AuditRetentionDays: *auditRetentionDaysFlag,
		}, closedWorld, ctxCommReg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Brique security runtime failed to initialize: %s\n", err)
			os.Exit(1)
		}
	}

	// brique-sec reads engine.pid to signal a reload; a failure only disables
	// `apply --reload`, never the engine.
	securityPIDFile := ""
	var securityPIDHandle *os.File
	securityDirectory := ""
	if locations.Machine != "" {
		if absolute, absErr := filepath.Abs(locations.Machine); absErr == nil {
			securityDirectory = absolute
			securityPIDFile = filepath.Join(securityDirectory, "engine.pid")
			handle, pidErr := acquireEnginePID(securityPIDFile)
			if pidErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: engine.pid not written: %s\n", pidErr)
				securityPIDFile = ""
			}
			securityPIDHandle = handle
		}
	}
	defer releaseEnginePID(securityPIDHandle, securityPIDFile)

	root, err := context.NewContextLoop(rootDir, rootID, ctxCommReg)
	if err != nil {
		_ = securityManager.Close()
		fmt.Fprintf(os.Stderr, "Brique root context failed to initialize: %s\n", err.Error())
		os.Exit(1)
	}
	if root == nil || root.State() == shared.ContextFailed {
		_ = securityManager.Close()
		fmt.Fprintln(os.Stderr, "Brique root context failed to initialize")
		os.Exit(1)
	}

	fmt.Printf("Brique Engine Started v%s. rootDir=%s rootId=%s\n", shared.EngineBinaryVersion, rootDir, rootID)

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	if securityManager != nil && securityDirectory != "" {
		// Where the platform has no SIGHUP, brique-sec asks for a reload through a file.
		defer startReloadWatcher(securityDirectory, sigCh)()
	}

	reloadRequests := make(chan struct{}, 1)
	reloadResults := make(chan error, 1)
	reloadStop := make(chan struct{})
	var reloadWorkers sync.WaitGroup
	if securityManager != nil {
		reloadWorkers.Add(1)
		go func() {
			defer reloadWorkers.Done()
			runSecurityReloadWorker(rootDir, securityManager, securitycatalog.Build, reloadRequests, reloadResults, reloadStop)
		}()
	}

	var shutdownSignal os.Signal
	for shutdownSignal == nil {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				if securityManager == nil {
					fmt.Println("Security reload ignored: security is not configured.")
					continue
				}
				select {
				case reloadRequests <- struct{}{}:
					fmt.Println("Security reload requested.")
				default:
					fmt.Println("Security reload coalesced with an outstanding request.")
				}
				continue
			}
			shutdownSignal = sig
		case err := <-reloadResults:
			if err == nil {
				fmt.Println("Security generation reloaded.")
				continue
			}
			fmt.Fprintf(os.Stderr, "Security reload rejected: %s\n", err)
			if errors.Is(err, securitymanager.ErrPublication) {
				shutdownSignal = syscall.SIGTERM
			}
		}
	}
	fmt.Printf("Stopping... (signal=%v)\n", shutdownSignal)

	go func() {
		for sig := range sigCh {
			if sig != syscall.SIGHUP {
				fmt.Println("Force exit.")
				os.Exit(1)
			}
		}
	}()

	close(reloadStop)
	reloadWorkers.Wait()
	root.Stop()
	if err := securityManager.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Security runtime shutdown failed: %s\n", err)
	}
	fmt.Println("Stopped.")
}
