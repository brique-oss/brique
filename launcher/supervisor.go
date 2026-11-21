/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"brique_engine/contracts/buildinfo"
	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/contracts/publication"
	"brique_engine/engine/localruntime"
	"brique_engine/platform/locking"
	"brique_engine/platform/protectedfile"
)

type State string

const (
	StateStopped          State = "stopped"
	StateResolvingRuntime State = "resolving_runtime"
	StateStartingEngine   State = "starting_engine"
	StateWaitingEngine    State = "waiting_engine"
	StateStartingWeb      State = "starting_web"
	StateReady            State = "ready"
	StateStopping         State = "stopping"
	StateFailed           State = "failed"
	StateInterrupted      State = "interrupted"
)

type Status struct {
	Alias       string `json:"alias"`
	State       State  `json:"state"`
	Supervision string `json:"supervision"`
	Owned       bool   `json:"owned"`
	PID         int    `json:"pid,omitempty"`
	Generation  string `json:"generation,omitempty"`
	UIWebSocket string `json:"ui_websocket_address,omitempty"`
	WebPID      int    `json:"web_pid,omitempty"`
	WebURL      string `json:"web_url,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	LogError    string `json:"log_error,omitempty"`
	Error       string `json:"error,omitempty"`
}

type SupervisorOptions struct {
	Store            Store
	RuntimeDirectory string
	StartTimeout     time.Duration
	StopTimeout      time.Duration
	PollInterval     time.Duration
}

type Supervisor struct {
	store            Store
	runtimeDirectory string
	startTimeout     time.Duration
	stopTimeout      time.Duration
	pollInterval     time.Duration

	mu       sync.Mutex
	entries  map[string]*supervisedProcess
	commands map[string]*sync.Mutex
}

type supervisedProcess struct {
	status       Status
	command      *exec.Cmd
	descriptor   localruntime.EngineRuntimeDescriptor
	done         chan struct{}
	exitError    error
	stopping     bool
	log          *os.File
	lifetime     *supervisorLifetime
	webCommand   *exec.Cmd
	webLifetime  *supervisorLifetime
	webDone      chan struct{}
	webExitError error
	webStopping  bool
	logStop      chan struct{}
	logDone      chan struct{}
	logCloseOnce sync.Once
}

func NewSupervisor(options SupervisorOptions) (*Supervisor, error) {
	runtimeDirectory, err := localruntime.ResolveDiscoveryDirectory(options.RuntimeDirectory)
	if err != nil {
		return nil, err
	}
	if options.Store.Directory == "" {
		return nil, errors.New("launcher store is required")
	}
	if options.StartTimeout <= 0 {
		options.StartTimeout = 20 * time.Second
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = 15 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 25 * time.Millisecond
	}
	return &Supervisor{store: options.Store, runtimeDirectory: runtimeDirectory,
		startTimeout: options.StartTimeout, stopTimeout: options.StopTimeout,
		pollInterval: options.PollInterval, entries: map[string]*supervisedProcess{}, commands: map[string]*sync.Mutex{}}, nil
}

func (supervisor *Supervisor) Start(ctx context.Context, alias string) (Status, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	return supervisor.startLocked(ctx, alias)
}

func (supervisor *Supervisor) startLocked(ctx context.Context, alias string) (Status, error) {
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Status{}, err
	}
	if status, err, handled := supervisor.existingEntryOutcome(alias); handled {
		return status, err
	}
	if err := verifyRegisteredObjects(instance); err != nil {
		return supervisor.failure(instance, err), err
	}
	if status, err, handled := supervisor.adoptRunningOrRefuse(instance, alias); handled {
		return status, err
	}
	if err := supervisor.verifyRuntime(ctx, instance); err != nil {
		return supervisor.failure(instance, err), err
	}
	if err := supervisor.verifyPublication(instance); err != nil {
		return supervisor.failure(instance, err), err
	}

	entry := &supervisedProcess{status: Status{Alias: alias, State: StateResolvingRuntime, Supervision: instance.Supervision, Owned: true}, done: make(chan struct{})}
	supervisor.mu.Lock()
	supervisor.entries[alias] = entry
	supervisor.mu.Unlock()

	if status, err := supervisor.spawnEngine(instance, alias, entry); err != nil {
		return status, err
	}

	descriptor, err := supervisor.waitReady(ctx, instance, entry)
	if err != nil {
		_ = supervisor.stopOwned(entry)
		return supervisor.failEntry(entry, err), err
	}
	supervisor.mu.Lock()
	entry.descriptor = descriptor
	entry.status.Generation = descriptor.Generation
	entry.status.UIWebSocket = descriptor.UIWebSocket
	entry.status.State = StateStartingWeb
	supervisor.mu.Unlock()
	if instance.PublicationPath != "" {
		if err := supervisor.startWeb(ctx, instance, entry); err != nil {
			_ = supervisor.stopOwned(entry)
			return supervisor.failEntry(entry, err), err
		}
	}
	supervisor.setState(entry, StateReady, "")
	return supervisor.status(entry), nil
}

// existingEntryOutcome answers a start request from the state already recorded for
// alias. handled is false when a fresh start must go on.
func (supervisor *Supervisor) existingEntryOutcome(alias string) (Status, error, bool) {
	supervisor.mu.Lock()
	if entry := supervisor.entries[alias]; entry != nil {
		if entry.status.State != StateStopped && entry.status.State != StateFailed {
			status := entry.status
			supervisor.mu.Unlock()
			if status.State == StateInterrupted {
				return status, errors.New(status.Error), true
			}
			return status, nil, true
		}
		if entry.status.State == StateFailed && entry.status.Owned && entry.command != nil {
			select {
			case <-entry.done:
			default:
				status := entry.status
				supervisor.mu.Unlock()
				return status, errors.New(status.Error), true
			}
		}
	}
	supervisor.mu.Unlock()
	return Status{}, nil, false
}

// adoptRunningOrRefuse records an engine already running outside this supervisor,
// or refuses to start an externally supervised instance. handled is false when
// this supervisor must launch the engine itself.
func (supervisor *Supervisor) adoptRunningOrRefuse(instance Instance, alias string) (Status, error, bool) {
	if descriptor, discoveryErr := supervisor.discover(instance); discoveryErr == nil {
		status := statusFromDescriptor(instance, descriptor, false)
		if instance.Supervision == SupervisionManaged {
			status.State = StateInterrupted
			status.Error = "engine is running outside this supervisor"
		}
		supervisor.mu.Lock()
		supervisor.entries[alias] = &supervisedProcess{status: status, descriptor: descriptor, done: closedChannel()}
		supervisor.mu.Unlock()
		if instance.Supervision == SupervisionManaged {
			return status, errors.New(status.Error), true
		}
		return status, nil, true
	}
	if instance.Supervision == SupervisionExternal {
		status := Status{Alias: alias, State: StateStopped, Supervision: instance.Supervision}
		return status, errors.New("external instance must be started by its declared supervisor"), true
	}
	return Status{}, nil, false
}

// spawnEngine opens the instance log, starts the engine process under the supervisor
// lifetime and leaves the entry in the waiting-engine state. A non-nil error comes
// with the failed status already recorded for the entry.
func (supervisor *Supervisor) spawnEngine(instance Instance, alias string, entry *supervisedProcess) (Status, error) {
	log, logPath, err := supervisor.openLog(alias)
	if err != nil {
		return supervisor.failEntry(entry, err), err
	}
	entry.log = log
	supervisor.mu.Lock()
	entry.status.LogPath = logPath
	supervisor.mu.Unlock()
	supervisor.startLogMonitor(entry)
	arguments := []string{"-root_path", instance.RootPath, "-runtime_dir", supervisor.runtimeDirectory, "-websocket_addr", "127.0.0.1:0"}
	if instance.PublicationPath != "" {
		arguments = append(arguments, "-web_publication", instance.PublicationPath)
	}
	if instance.SecurityDirectory != "" {
		arguments = append(arguments, "-security_dir", instance.SecurityDirectory)
	}
	command := exec.Command(instance.EnginePath, arguments...)
	// Use the file directly: an os/exec copy pipe would disappear with a hard
	// launcher crash and could SIGPIPE the engine before its ordered teardown.
	command.Stdout, command.Stderr = log, log
	entry.command = command
	lifetime, err := configureSupervisorLifetime(command)
	if err != nil {
		supervisor.closeLog(alias, entry)
		return supervisor.failEntry(entry, fmt.Errorf("create supervisor lifetime: %w", err)), err
	}
	entry.lifetime = lifetime
	supervisor.setState(entry, StateStartingEngine, "")
	if err := command.Start(); err != nil {
		_ = lifetime.close()
		supervisor.closeLog(alias, entry)
		return supervisor.failEntry(entry, fmt.Errorf("start engine: %w", err)), err
	}
	if err := lifetime.afterStart(); err != nil {
		_ = lifetime.close()
		_ = command.Process.Kill()
		_ = command.Wait()
		supervisor.closeLog(alias, entry)
		return supervisor.failEntry(entry, fmt.Errorf("release inherited lifetime handle: %w", err)), err
	}
	supervisor.mu.Lock()
	entry.status.PID = command.Process.Pid
	supervisor.mu.Unlock()
	go supervisor.reap(alias, entry)
	supervisor.setState(entry, StateWaitingEngine, "")
	return Status{}, nil
}

func (supervisor *Supervisor) Stop(ctx context.Context, alias string) (Status, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	return supervisor.stopLocked(ctx, alias)
}

func (supervisor *Supervisor) stopLocked(ctx context.Context, alias string) (Status, error) {
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Status{}, err
	}
	if instance.Supervision == SupervisionExternal {
		status, _ := supervisor.Status(ctx, alias)
		return status, errors.New("external instance is not owned by the launcher")
	}
	supervisor.mu.Lock()
	entry := supervisor.entries[alias]
	if entry == nil || !entry.status.Owned || entry.command == nil {
		supervisor.mu.Unlock()
		if descriptor, discoverErr := supervisor.discover(instance); discoverErr == nil {
			status := statusFromDescriptor(instance, descriptor, false)
			status.State, status.Error = StateInterrupted, "running engine is not owned by this supervisor"
			return status, errors.New(status.Error)
		}
		return Status{Alias: alias, State: StateStopped, Supervision: instance.Supervision}, nil
	}
	select {
	case <-entry.done:
		entry.status.State, entry.status.Error = StateStopped, ""
		status := entry.status
		supervisor.mu.Unlock()
		return status, nil
	default:
	}
	if entry.status.State == StateStopped {
		status := entry.status
		supervisor.mu.Unlock()
		return status, nil
	}
	entry.stopping = true
	entry.status.State, entry.status.Error = StateStopping, ""
	descriptor := entry.descriptor
	done := entry.done
	supervisor.mu.Unlock()

	webStopErr := supervisor.stopWeb(entry)

	if descriptor.Generation == "" {
		if discovered, discoverErr := supervisor.discover(instance); discoverErr == nil && discovered.PID == entry.command.Process.Pid {
			descriptor = discovered
		}
	}
	if descriptor.Generation != "" {
		_, _ = localruntime.CallControl(descriptor, localruntime.ControlRequest{Format: localruntime.LocalControlProtocol,
			Generation: descriptor.Generation, Operation: localruntime.OperationStop})
	}

	timer := time.NewTimer(supervisor.stopTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		_ = entry.command.Process.Kill()
		<-done
		return supervisor.status(entry), ctx.Err()
	case <-timer.C:
		_ = entry.command.Process.Kill()
		<-done
	}
	status := supervisor.status(entry)
	if webStopErr != nil {
		return status, webStopErr
	}
	return status, nil
}

func (supervisor *Supervisor) Restart(ctx context.Context, alias string) (Status, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Status{}, err
	}
	if instance.Supervision != SupervisionManaged {
		status, _ := supervisor.Status(ctx, alias)
		return status, errors.New("external instance is not owned by the launcher")
	}
	if _, err := supervisor.stopLocked(ctx, alias); err != nil {
		return Status{}, err
	}
	if err := ctx.Err(); err != nil {
		return Status{Alias: alias, State: StateStopped, Supervision: instance.Supervision}, err
	}
	return supervisor.startLocked(ctx, alias)
}

func (supervisor *Supervisor) UpdateEngine(ctx context.Context, alias, enginePath string) (Instance, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Instance{}, err
	}
	if _, discoverErr := supervisor.discover(instance); discoverErr == nil {
		return Instance{}, errors.New("engine update requires a stopped instance")
	}
	supervisor.mu.Lock()
	entry := supervisor.entries[alias]
	running := false
	if entry != nil && entry.done != nil {
		select {
		case <-entry.done:
		default:
			running = true
		}
	}
	supervisor.mu.Unlock()
	if running {
		return Instance{}, errors.New("engine update requires a stopped instance")
	}
	updated, err := supervisor.store.UpdateEngine(ctx, alias, enginePath)
	if err != nil {
		return Instance{}, err
	}
	supervisor.mu.Lock()
	delete(supervisor.entries, alias)
	supervisor.mu.Unlock()
	return updated, nil
}

func (supervisor *Supervisor) UpdateRuntime(ctx context.Context, alias, runtimePath string) (Instance, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Instance{}, err
	}
	if _, discoverErr := supervisor.discover(instance); discoverErr == nil {
		return Instance{}, errors.New("runtime update requires a stopped instance")
	}
	supervisor.mu.Lock()
	entry := supervisor.entries[alias]
	running := false
	if entry != nil && entry.done != nil {
		select {
		case <-entry.done:
		default:
			running = true
		}
	}
	supervisor.mu.Unlock()
	if running {
		return Instance{}, errors.New("runtime update requires a stopped instance")
	}
	updated, err := supervisor.store.UpdateRuntime(ctx, alias, runtimePath)
	if err != nil {
		return Instance{}, err
	}
	supervisor.mu.Lock()
	delete(supervisor.entries, alias)
	supervisor.mu.Unlock()
	return updated, nil
}

func (supervisor *Supervisor) UpdatePublication(ctx context.Context, alias, publicationPath string) (Instance, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Instance{}, err
	}
	if _, discoverErr := supervisor.discover(instance); discoverErr == nil {
		return Instance{}, errors.New("publication update requires a stopped instance")
	}
	supervisor.mu.Lock()
	entry := supervisor.entries[alias]
	running := false
	if entry != nil && entry.done != nil {
		select {
		case <-entry.done:
		default:
			running = true
		}
	}
	supervisor.mu.Unlock()
	if running {
		return Instance{}, errors.New("publication update requires a stopped instance")
	}
	updated, err := supervisor.store.UpdatePublication(ctx, alias, publicationPath)
	if err != nil {
		return Instance{}, err
	}
	supervisor.mu.Lock()
	delete(supervisor.entries, alias)
	supervisor.mu.Unlock()
	return updated, nil
}

// UpdateWebProfile changes the public posture of the web host of a stopped
// instance. The profile is never modified under a running web host.
func (supervisor *Supervisor) UpdateWebProfile(_ context.Context, alias string, profile *WebProfile) (Instance, error) {
	commandLock := supervisor.commandLock(alias)
	commandLock.Lock()
	defer commandLock.Unlock()
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Instance{}, err
	}
	if _, discoverErr := supervisor.discover(instance); discoverErr == nil {
		return Instance{}, errors.New("web profile update requires a stopped instance")
	}
	supervisor.mu.Lock()
	entry := supervisor.entries[alias]
	running := false
	if entry != nil && entry.done != nil {
		select {
		case <-entry.done:
		default:
			running = true
		}
	}
	supervisor.mu.Unlock()
	if running {
		return Instance{}, errors.New("web profile update requires a stopped instance")
	}
	updated, err := supervisor.store.UpdateWebProfile(alias, profile)
	if err != nil {
		return Instance{}, err
	}
	supervisor.mu.Lock()
	delete(supervisor.entries, alias)
	supervisor.mu.Unlock()
	return updated, nil
}

func (supervisor *Supervisor) Status(_ context.Context, alias string) (Status, error) {
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return Status{}, err
	}
	supervisor.mu.Lock()
	entry := supervisor.entries[alias]
	if entry != nil && entry.status.State == StateFailed && entry.status.Owned && entry.command != nil {
		select {
		case <-entry.done:
		default:
			status := entry.status
			supervisor.mu.Unlock()
			return status, nil
		}
	}
	if entry != nil && entry.status.State != StateStopped && entry.status.State != StateFailed && entry.status.State != StateInterrupted {
		status := entry.status
		supervisor.mu.Unlock()
		return status, nil
	}
	supervisor.mu.Unlock()
	if descriptor, discoverErr := supervisor.discover(instance); discoverErr == nil {
		status := statusFromDescriptor(instance, descriptor, false)
		if instance.Supervision == SupervisionManaged {
			status.State, status.Error = StateInterrupted, "engine is running outside this supervisor"
		}
		return status, nil
	}
	if entry != nil {
		return supervisor.status(entry), nil
	}
	return Status{Alias: alias, State: StateStopped, Supervision: instance.Supervision}, nil
}

// DiagnoseConnection asks the running engine of an instance to diagnose its connection to a
// declared peer. The launcher has no network logic: it finds the engine, forwards two names
// (a peer and an optional endpoint name of that peer) over the engine's owner-only control
// socket and returns what the engine answers.
func (supervisor *Supervisor) DiagnoseConnection(_ context.Context, alias, peer, endpoint string) (connectiondiagnosis.Result, error) {
	instance, err := supervisor.store.Get(alias)
	if err != nil {
		return connectiondiagnosis.Result{}, err
	}
	descriptor, err := supervisor.discover(instance)
	if err != nil {
		return connectiondiagnosis.Result{}, errors.New("the engine of this instance is not running")
	}
	response, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{Format: localruntime.LocalControlProtocol,
		Generation: descriptor.Generation, Operation: localruntime.OperationDiagnoseConnection, Peer: peer, Endpoint: endpoint})
	if err != nil {
		return connectiondiagnosis.Result{}, errors.New("the engine did not answer the diagnosis")
	}
	if !response.OK || response.Diagnosis == nil || !response.Diagnosis.Valid() {
		return connectiondiagnosis.Result{}, errors.New("the engine refused the diagnosis")
	}
	return *response.Diagnosis, nil
}

func (supervisor *Supervisor) Statuses(ctx context.Context) ([]Status, error) {
	registry, err := supervisor.store.List()
	if err != nil {
		return nil, err
	}
	statuses := make([]Status, 0, len(registry.Instances))
	for _, instance := range registry.Instances {
		status, err := supervisor.Status(ctx, instance.Alias)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (supervisor *Supervisor) StartAutostart(ctx context.Context) map[string]error {
	registry, err := supervisor.store.List()
	if err != nil {
		return map[string]error{"registry": err}
	}
	failures := map[string]error{}
	for _, instance := range registry.Instances {
		if !instance.Autostart || instance.Supervision != SupervisionManaged {
			continue
		}
		if _, err := supervisor.Start(ctx, instance.Alias); err != nil {
			failures[instance.Alias] = err
		}
	}
	return failures
}

func (supervisor *Supervisor) Shutdown(ctx context.Context) error {
	registry, err := supervisor.store.List()
	if err != nil {
		return err
	}
	var joined error
	for _, instance := range registry.Instances {
		if instance.Supervision != SupervisionManaged {
			continue
		}
		if _, err := supervisor.Stop(ctx, instance.Alias); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (supervisor *Supervisor) waitReady(ctx context.Context, instance Instance, entry *supervisedProcess) (localruntime.EngineRuntimeDescriptor, error) {
	timer := time.NewTimer(supervisor.startTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(supervisor.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return localruntime.EngineRuntimeDescriptor{}, ctx.Err()
		case <-entry.done:
			return localruntime.EngineRuntimeDescriptor{}, fmt.Errorf("engine exited before readiness: %v", entry.exitError)
		case <-timer.C:
			return localruntime.EngineRuntimeDescriptor{}, errors.New("engine readiness timed out")
		case <-ticker.C:
			descriptor, err := supervisor.discover(instance)
			if err != nil || descriptor.PID != entry.command.Process.Pid || descriptor.State != localruntime.StateReady {
				continue
			}
			return descriptor, nil
		}
	}
}

func (supervisor *Supervisor) discover(instance Instance) (localruntime.EngineRuntimeDescriptor, error) {
	key, err := localruntime.DiscoveryKey(instance.RootObject)
	if err != nil {
		return localruntime.EngineRuntimeDescriptor{}, err
	}
	descriptor, err := localruntime.LoadDescriptor(supervisor.runtimeDirectory, key)
	if err != nil || descriptor.RootObject.Key() != instance.RootObject.Key() || descriptor.RootPath != instance.RootPath {
		return localruntime.EngineRuntimeDescriptor{}, errors.New("running engine not discovered")
	}
	response, err := localruntime.CallControl(descriptor, localruntime.ControlRequest{Format: localruntime.LocalControlProtocol,
		Generation: descriptor.Generation, Operation: localruntime.OperationStatus})
	if err != nil || !response.OK || response.PID != descriptor.PID {
		return localruntime.EngineRuntimeDescriptor{}, errors.New("running engine control verification failed")
	}
	return descriptor, nil
}

func (supervisor *Supervisor) reap(alias string, entry *supervisedProcess) {
	err := entry.command.Wait()
	if entry.lifetime != nil {
		_ = entry.lifetime.close()
	}
	_ = supervisor.stopWeb(entry)
	supervisor.closeLog(alias, entry)
	supervisor.mu.Lock()
	entry.exitError = err
	entry.status.PID, entry.status.Generation, entry.status.UIWebSocket = 0, "", ""
	if entry.stopping && (err == nil || exitWasForced(err)) {
		entry.status.State, entry.status.Error = StateStopped, ""
	} else if err == nil {
		entry.status.State, entry.status.Error = StateInterrupted, "engine exited without a stop request"
	} else {
		entry.status.State, entry.status.Error = StateFailed, err.Error()
	}
	close(entry.done)
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) stopOwned(entry *supervisedProcess) error {
	supervisor.mu.Lock()
	entry.stopping = true
	done := entry.done
	process := entry.command.Process
	supervisor.mu.Unlock()
	_ = process.Kill()
	select {
	case <-done:
		return nil
	case <-time.After(supervisor.stopTimeout):
		return errors.New("engine did not exit after kill")
	}
}

func (supervisor *Supervisor) setState(entry *supervisedProcess, state State, message string) {
	supervisor.mu.Lock()
	entry.status.State, entry.status.Error = state, message
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) commandLock(alias string) *sync.Mutex {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	lock := supervisor.commands[alias]
	if lock == nil {
		lock = &sync.Mutex{}
		supervisor.commands[alias] = lock
	}
	return lock
}

func (supervisor *Supervisor) failEntry(entry *supervisedProcess, err error) Status {
	supervisor.setState(entry, StateFailed, err.Error())
	return supervisor.status(entry)
}

func (supervisor *Supervisor) failure(instance Instance, err error) Status {
	status := Status{Alias: instance.Alias, State: StateFailed, Supervision: instance.Supervision, Error: err.Error()}
	supervisor.mu.Lock()
	supervisor.entries[instance.Alias] = &supervisedProcess{status: status, done: closedChannel()}
	supervisor.mu.Unlock()
	return status
}

func (supervisor *Supervisor) status(entry *supervisedProcess) Status {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return entry.status
}

func (supervisor *Supervisor) openLog(alias string) (*os.File, string, error) {
	if err := supervisor.pruneLogs(alias, time.Now().UTC()); err != nil {
		return nil, "", err
	}
	launch := time.Now().UTC().Format("20060102T150405.000000000Z")
	directory := filepath.Join(supervisor.store.Directory, "logs", alias)
	if err := protectedfile.CreateProtectedDirectory(directory); err != nil {
		return nil, "", err
	}
	if err := protectedfile.ValidateProtectedDirectory(directory, nil); err != nil {
		return nil, "", err
	}
	path := filepath.Join(directory, launch+".log")
	file, err := protectedfile.OpenProtectedFile(path, true, nil, nil)
	return file, path, err
}

func verifyRegisteredObjects(instance Instance) error {
	root, err := locking.DirectoryObjectIdentity(instance.RootPath)
	if err != nil || root.Key() != instance.RootObject.Key() {
		return errors.New("registered root object has changed")
	}
	if instance.SecurityObject != nil {
		security, err := locking.DirectoryObjectIdentity(instance.SecurityDirectory)
		if err != nil || security.Key() != instance.SecurityObject.Key() {
			return errors.New("registered security directory object has changed")
		}
	}
	return nil
}

func statusFromDescriptor(instance Instance, descriptor localruntime.EngineRuntimeDescriptor, owned bool) Status {
	state := StateReady
	switch descriptor.State {
	case localruntime.StateStarting:
		state = StateWaitingEngine
	case localruntime.StateStopping:
		state = StateStopping
	case localruntime.StateFailed:
		state = StateFailed
	}
	return Status{Alias: instance.Alias, State: state, Supervision: instance.Supervision, Owned: owned,
		PID: descriptor.PID, Generation: descriptor.Generation, UIWebSocket: descriptor.UIWebSocket}
}

func (supervisor *Supervisor) verifyRuntime(ctx context.Context, instance Instance) error {
	if instance.RuntimePath != "" {
		inspect := supervisor.store.InspectPackage
		if inspect == nil {
			inspect = InspectRuntimePackage
		}
		inspection, err := inspect(ctx, instance.RuntimePath)
		if err != nil {
			return fmt.Errorf("registered runtime package is unavailable: %w", err)
		}
		if !inspection.Compatible || inspection.Manifest.RuntimeID != instance.RuntimeID || inspection.Directory != instance.RuntimePath ||
			!sameBuildInfo(inspection.Manifest.EngineBuild, instance.Build) {
			return errors.New("registered runtime package has changed or is incompatible")
		}
		enginePath, err := filepath.EvalSymlinks(filepath.Join(inspection.Directory,
			filepath.FromSlash(inspection.Manifest.Entrypoints.Engine)))
		if err != nil || enginePath != instance.EnginePath {
			return errors.New("registered runtime engine path has changed")
		}
		return nil
	}
	inspect := supervisor.store.Inspect
	if inspect == nil {
		inspect = InspectRuntime
	}
	build, err := inspect(ctx, instance.EnginePath)
	if err != nil {
		return fmt.Errorf("registered engine is unavailable: %w", err)
	}
	if !sameBuildInfo(build, instance.Build) {
		return errors.New("registered engine provenance has changed")
	}
	return nil
}

func (supervisor *Supervisor) verifyPublication(instance Instance) error {
	if instance.PublicationPath == "" {
		return nil
	}
	inspect := supervisor.store.InspectPublication
	if inspect == nil {
		inspect = publication.Inspect
	}
	inspection, err := inspect(instance.PublicationPath)
	if err != nil {
		return fmt.Errorf("registered web publication is unavailable: %w", err)
	}
	if inspection.Directory != instance.PublicationPath || inspection.PublicationID != instance.PublicationID {
		return errors.New("registered web publication has changed")
	}
	return nil
}

// webHostArguments derives the web host command line from the persisted profile
// alone; nothing else decides the public authority, origin or trusted proxy.
func webHostArguments(instance Instance) []string {
	arguments := []string{"-publication", instance.PublicationPath, "-addr", "127.0.0.1:0"}
	profile := instance.Web
	if profile == nil {
		return arguments
	}
	if profile.Address != "" {
		arguments[3] = profile.Address
	}
	for _, host := range profile.AllowedHosts {
		arguments = append(arguments, "-allowed_host", host)
	}
	for _, origin := range profile.PublicOrigins {
		arguments = append(arguments, "-public_origin", origin)
	}
	for _, proxy := range profile.TrustedProxies {
		arguments = append(arguments, "-trusted_proxy", proxy)
	}
	return arguments
}

func (supervisor *Supervisor) startWeb(ctx context.Context, instance Instance, entry *supervisedProcess) error {
	// Engine readiness can take seconds. Recheck both immutable inputs at the
	// actual web-host boundary rather than relying only on the pre-engine check.
	if err := supervisor.verifyRuntime(ctx, instance); err != nil {
		return err
	}
	if err := supervisor.verifyPublication(instance); err != nil {
		return err
	}
	binary, err := supervisor.webHostPath(instance)
	if err != nil {
		return err
	}
	arguments := webHostArguments(instance)
	if entry.descriptor.PublicWebSocket != "" {
		arguments = append(arguments, "-public_web_backend", entry.descriptor.PublicWebSocket)
	}
	command := exec.Command(binary, arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("prepare web host output: %w", err)
	}
	command.Stderr = entry.log
	lifetime, err := configureSupervisorLifetime(command)
	if err != nil {
		return fmt.Errorf("create web host supervisor lifetime: %w", err)
	}
	ready := make(chan webReadyResult, 1)
	entry.webCommand, entry.webLifetime = command, lifetime
	entry.webDone = make(chan struct{})
	if err := command.Start(); err != nil {
		_ = lifetime.close()
		return fmt.Errorf("start web host: %w", err)
	}
	if err := lifetime.afterStart(); err != nil {
		_ = lifetime.close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("release web host lifetime handle: %w", err)
	}
	supervisor.mu.Lock()
	entry.status.WebPID = command.Process.Pid
	supervisor.mu.Unlock()
	go copyWebReady(stdout, entry.log, ready)
	go supervisor.reapWeb(entry)
	timer := time.NewTimer(supervisor.startTimeout)
	defer timer.Stop()
	select {
	case result := <-ready:
		if result.err != nil {
			_ = supervisor.stopWeb(entry)
			return result.err
		}
		supervisor.mu.Lock()
		entry.status.WebURL = result.url
		supervisor.mu.Unlock()
		return nil
	case <-entry.webDone:
		supervisor.mu.Lock()
		exitError := entry.webExitError
		supervisor.mu.Unlock()
		return fmt.Errorf("web host exited before readiness: %v", exitError)
	case <-entry.done:
		_ = supervisor.stopWeb(entry)
		return errors.New("engine exited while web host was starting")
	case <-ctx.Done():
		_ = supervisor.stopWeb(entry)
		return ctx.Err()
	case <-timer.C:
		_ = supervisor.stopWeb(entry)
		return errors.New("web host readiness timed out")
	}
}

type webReadyResult struct {
	url string
	err error
}

func copyWebReady(reader io.Reader, log *os.File, ready chan<- webReadyResult) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 64<<10))
	scanner.Buffer(make([]byte, 1024), 16<<10)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if log != nil {
			_, _ = fmt.Fprintln(log, line)
		}
		if found {
			continue
		}
		const prefix = "Brique web host ready: "
		if strings.HasPrefix(line, prefix) {
			url := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if strings.HasPrefix(url, "http://127.0.0.1:") && strings.HasSuffix(url, "/") {
				found = true
				ready <- webReadyResult{url: url}
			}
		}
	}
	if !found {
		if err := scanner.Err(); err != nil {
			ready <- webReadyResult{err: fmt.Errorf("read web host readiness: %w", err)}
		} else {
			ready <- webReadyResult{err: errors.New("web host closed output before readiness")}
		}
	}
}

func (supervisor *Supervisor) reapWeb(entry *supervisedProcess) {
	err := entry.webCommand.Wait()
	if entry.webLifetime != nil {
		_ = entry.webLifetime.close()
	}
	supervisor.mu.Lock()
	entry.webExitError = err
	entry.status.WebPID, entry.status.WebURL = 0, ""
	if !entry.webStopping && !entry.stopping {
		entry.status.State = StateFailed
		entry.status.Error = fmt.Sprintf("web host exited unexpectedly: %v", err)
	}
	close(entry.webDone)
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) stopWeb(entry *supervisedProcess) error {
	supervisor.mu.Lock()
	if entry.webCommand == nil || entry.webDone == nil {
		supervisor.mu.Unlock()
		return nil
	}
	select {
	case <-entry.webDone:
		supervisor.mu.Unlock()
		return nil
	default:
	}
	entry.webStopping = true
	process, done := entry.webCommand.Process, entry.webDone
	supervisor.mu.Unlock()
	if err := process.Signal(os.Interrupt); err != nil {
		_ = process.Kill()
	}
	timer := time.NewTimer(supervisor.stopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		_ = process.Kill()
		<-done
		return errors.New("web host required a forced stop")
	}
}

func (supervisor *Supervisor) webHostPath(instance Instance) (string, error) {
	if instance.RuntimePath == "" {
		return "", errors.New("web publication requires a packaged runtime")
	}
	manifest, err := loadRuntimeManifest(filepath.Join(instance.RuntimePath, RuntimeManifestName))
	if err != nil {
		return "", errors.New("runtime manifest is unavailable for web host")
	}
	if err := validateWebRuntimeManifest(manifest); err != nil {
		return "", err
	}
	for _, component := range manifest.Components {
		if component.Name != "web-host" || component.Kind != "binary" {
			continue
		}
		binary, err := resolveFile(filepath.Join(instance.RuntimePath, filepath.FromSlash(component.Path)))
		if err != nil {
			return "", errors.New("runtime web host is unavailable")
		}
		return binary, nil
	}
	return "", errors.New("runtime has no web-host component")
}

func sameBuildInfo(left, right buildinfo.Info) bool {
	if left.Format != right.Format || left.EngineVersion != right.EngineVersion || left.VCSRevision != right.VCSRevision ||
		left.VCSTime != right.VCSTime || left.GoVersion != right.GoVersion || left.OS != right.OS || left.Arch != right.Arch {
		return false
	}
	if left.VCSModified == nil || right.VCSModified == nil {
		return left.VCSModified == nil && right.VCSModified == nil
	}
	return *left.VCSModified == *right.VCSModified
}

func closedChannel() chan struct{} {
	result := make(chan struct{})
	close(result)
	return result
}

func exitWasForced(err error) bool { return err != nil }
