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

	"brique_engine/contracts/publication"
	comm "brique_engine/engine/communication"
	context "brique_engine/engine/context"
	"brique_engine/engine/junction"
	"brique_engine/engine/localruntime"
	securitycatalog "brique_engine/engine/security/catalog"
	"brique_engine/engine/security/hostdir"
	securitymanager "brique_engine/engine/security/manager"
	"brique_engine/engine/security/trust"
	"brique_engine/engine/shared"
	"brique_engine/platform/locking"
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
	runtimeDirectoryFlag := flag.String("runtime_dir", "", "Private machine-local runtime directory for discovery and control (optional)")
	webSocketAddrFlag := flag.String("websocket_addr", "", "Host-local loopback override for the shared WebSocket listener (for example 127.0.0.1:0)")
	webPublicationFlag := flag.String("web_publication", "", "Verified immutable Spark web publication associated with this instance")
	auditMaxFileMiBFlag := flag.Int("audit_max_file_mib", 0, "Maximum size in MiB of one denial-journal file (default 64)")
	auditRetentionDaysFlag := flag.Int("audit_retention_days", 0, "Days of denial journal kept (default 14)")
	buildInfoJSONFlag := flag.Bool("build_info_json", false, "Print brique.build-info/1 as JSON and exit")
	supervisorLifetimeHandleFlag := flag.String("supervisor_lifetime_handle", "", "Inherited launcher lifetime handle (launcher-managed engines only)")
	flag.Parse()
	if *buildInfoJSONFlag {
		if err := writeBuildInfoJSON(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Brique build information unavailable: %s\n", err)
			os.Exit(1)
		}
		return
	}
	supervisorLost, closeSupervisorLifetime, err := startSupervisorLifetime(*supervisorLifetimeHandleFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start supervisor lifetime: %s\n", err)
		os.Exit(1)
	}
	defer closeSupervisorLifetime()

	rootDir := ""
	if *rootPathFlag != "" {
		rootDir = *rootPathFlag
	}
	if rootDir == "" {
		fmt.Fprintf(os.Stderr, "Brique failed to start invalid empty root dir\n")
		os.Exit(1)
	}
	canonicalRoot, err := canonicalExistingDirectory(rootDir, "root")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start invalid root dir: %s\n", err)
		os.Exit(1)
	}
	rootDir = canonicalRoot

	rootID := shared.RootContextID

	ctxCommReg := junction.NewContextCommRegistry()
	if *webPublicationFlag != "" {
		inspection, inspectErr := publication.Inspect(*webPublicationFlag)
		if inspectErr != nil {
			fmt.Fprintf(os.Stderr, "Brique failed to start: invalid web publication: %s\n", inspectErr)
			os.Exit(1)
		}
		published := make([]junction.PublishedUI, 0, len(inspection.UIs))
		for _, ui := range inspection.UIs {
			published = append(published, junction.PublishedUI{ContextPath: shared.ContextAddr(ui.ContextPath), UIName: ui.UIName, URL: ui.URL})
		}
		if installErr := ctxCommReg.InstallPublishedUIInventory(inspection.PublicationID, published); installErr != nil {
			fmt.Fprintf(os.Stderr, "Brique failed to start: install web publication: %s\n", installErr)
			os.Exit(1)
		}
	}
	if err := ctxCommReg.SetWebSocketListenerAddrOverride(*webSocketAddrFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start: %s\n", err)
		os.Exit(1)
	}
	var securityManager *securitymanager.Manager
	locations, err := hostdir.Resolve(rootDir, *securityDirectoryFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start: %s\n", err)
		os.Exit(1)
	}
	if locations.Machine == "" && *securityGenerationFlag != "" {
		fmt.Fprintln(os.Stderr, "Brique failed to start: security_generation requires a security directory")
		os.Exit(1)
	}
	if locations.Machine != "" {
		canonicalSecurity, canonicalErr := canonicalExistingDirectory(locations.Machine, "security")
		if canonicalErr != nil {
			fmt.Fprintf(os.Stderr, "Brique failed to start: the security directory %s does not exist; create it with brique-sec init\n", locations.Machine)
			os.Exit(1)
		}
		locations.Machine = canonicalSecurity
	}

	instanceLocks, err := acquireInstanceDirectoryLocks(rootDir, locations.Machine)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start: %s\n", err)
		os.Exit(1)
	}
	defer instanceLocks.Close()

	ctxCommReg.SetMachineDirectory(locations.Machine)

	// Keep compatibility with running engines that predate directory-object
	// locks: their engine.pid lock must also exclude this startup. An unavailable
	// PID file is ambiguous and therefore fails closed before any security store.
	securityPIDFile := ""
	var securityPIDHandle *os.File
	securityDirectory := ""
	if locations.Machine != "" {
		securityDirectory = locations.Machine
		securityPIDFile = filepath.Join(securityDirectory, "engine.pid")
		handle, pidErr := acquireEnginePID(securityPIDFile)
		if pidErr != nil {
			if errors.Is(pidErr, errEnginePIDOwned) {
				fmt.Fprintf(os.Stderr, "Brique failed to start: security directory is already owned: %s\n", pidErr)
			} else {
				fmt.Fprintf(os.Stderr, "Brique failed to start: engine.pid is unavailable: %s\n", pidErr)
			}
			os.Exit(1)
		}
		securityPIDHandle = handle
	}
	defer releaseEnginePID(securityPIDHandle, securityPIDFile)

	runtimeDirectory, err := localruntime.ResolveDiscoveryDirectory(*runtimeDirectoryFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start local control: %s\n", err)
		os.Exit(1)
	}
	rootObject, err := locking.DirectoryObjectIdentity(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to identify root for local control: %s\n", err)
		os.Exit(1)
	}
	runtimeGeneration, err := localruntime.NewGeneration()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to create local control generation: %s\n", err)
		os.Exit(1)
	}
	localControl, err := localruntime.StartControl(localruntime.ControlOptions{
		DiscoveryDirectory: runtimeDirectory, RootPath: rootDir,
		RootObject: rootObject, Generation: runtimeGeneration,
		Diagnose: ctxCommReg.DiagnoseConnection,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Brique failed to start local control: %s\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := localControl.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Local control shutdown failed: %s\n", err)
		}
	}()

	if locations.Machine != "" {
		closedWorld, err := securitycatalog.Build(rootDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Brique security catalog failed to initialize: %s\n", err)
			_ = localControl.Close()
			os.Exit(1)
		}
		securityManager, err = securitymanager.Start(securitymanager.Options{
			SecurityDirectory: locations.Machine, PolicyDirectory: locations.Policy, InitialGeneration: *securityGenerationFlag,
			RequireReadOnlyKeyFiles: true,
			AuditMaxFileMiB:         *auditMaxFileMiBFlag, AuditRetentionDays: *auditRetentionDaysFlag,
		}, closedWorld, ctxCommReg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Brique security runtime failed to initialize: %s\n", err)
			_ = localControl.Close()
			os.Exit(1)
		}
	}

	root, err := context.NewContextLoop(rootDir, rootID, ctxCommReg)
	if err != nil {
		_ = securityManager.Close()
		fmt.Fprintf(os.Stderr, "Brique root context failed to initialize: %s\n", err.Error())
		_ = localControl.Close()
		os.Exit(1)
	}
	if root == nil || root.State() == shared.ContextFailed {
		_ = securityManager.Close()
		fmt.Fprintln(os.Stderr, "Brique root context failed to initialize")
		_ = localControl.Close()
		os.Exit(1)
	}
	if supervisorLifetimeAlreadyLost(supervisorLost) {
		root.Stop()
		_ = securityManager.Close()
		fmt.Fprintln(os.Stderr, "Brique launcher supervisor was lost during startup")
		_ = localControl.Close()
		os.Exit(1)
	}
	browserBridge, err := startBrowserBridge(root, runtimeDirectory, localControl)
	if err != nil {
		root.Stop()
		_ = securityManager.Close()
		fmt.Fprintf(os.Stderr, "Brique browser bridge failed to start: %s\n", err)
		_ = localControl.Close()
		os.Exit(1)
	}
	if browserBridge != nil {
		defer browserBridge.Close()
	}
	var publicWeb *comm.PublicWebServer
	if *webPublicationFlag != "" && ctxCommReg.SecurityRuntime() != nil {
		publicWeb, err = startPublicWeb(root, localControl)
		if err != nil {
			if browserBridge != nil {
				_ = browserBridge.Close()
			}
			root.Stop()
			_ = securityManager.Close()
			fmt.Fprintf(os.Stderr, "Brique public web ingress failed to start: %s\n", err)
			_ = localControl.Close()
			os.Exit(1)
		}
		defer publicWeb.Close()
	}
	if err := localControl.SetUIWebSocketAddress(ctxCommReg.WebSocketListenerAddr()); err != nil {
		if browserBridge != nil {
			_ = browserBridge.Close()
		}
		if publicWeb != nil {
			_ = publicWeb.Close()
		}
		root.Stop()
		_ = securityManager.Close()
		fmt.Fprintf(os.Stderr, "Brique local control failed to publish UI address: %s\n", err)
		_ = localControl.Close()
		os.Exit(1)
	}
	if err := localControl.SetState(localruntime.StateReady); err != nil {
		_ = browserBridge.Close()
		root.Stop()
		_ = securityManager.Close()
		fmt.Fprintf(os.Stderr, "Brique local control failed to publish readiness: %s\n", err)
		_ = localControl.Close()
		os.Exit(1)
	}

	fmt.Printf("Brique Engine Started v%s. rootDir=%s rootId=%s generation=%s\n", shared.EngineBinaryVersion, rootDir, rootID, runtimeGeneration)

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
		case <-localControl.StopRequested():
			shutdownSignal = syscall.SIGTERM
		case <-supervisorLost:
			shutdownSignal = syscall.SIGTERM
		}
	}
	fmt.Printf("Stopping... (signal=%v)\n", shutdownSignal)
	if err := localControl.SetState(localruntime.StateStopping); err != nil {
		fmt.Fprintf(os.Stderr, "Local control failed to publish stopping state: %s\n", err)
	}

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
	if browserBridge != nil {
		if err := browserBridge.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Browser bridge shutdown failed: %s\n", err)
		}
	}
	if publicWeb != nil {
		if err := publicWeb.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Public web shutdown failed: %s\n", err)
		}
	}
	root.Stop()
	if err := securityManager.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Security runtime shutdown failed: %s\n", err)
	}
	fmt.Println("Stopped.")
}
