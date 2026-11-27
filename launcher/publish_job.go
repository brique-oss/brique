/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PublishTools says whether this launcher can publish the web UIs of an instance: it needs the
// repository it was built from (the publication script and the Spark build) and Node.
type PublishTools struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	repository string
	node       string
}

// DetectPublishTools looks for the repository next to the launcher binary (bin/ of the clone).
func DetectPublishTools(executable string) PublishTools {
	repository := filepath.Join(filepath.Dir(executable), "..")
	tools := PublishTools{repository: repository}
	switch {
	case !isFile(filepath.Join(repository, "scripts", "publish-spark-web.mjs")):
		tools.Reason = "the publication script was not found next to the launcher"
	case !isFile(filepath.Join(repository, "dist", "spark", "index.js")):
		tools.Reason = "the interface is not built: run npm run setup"
	default:
		node, err := exec.LookPath("node")
		if err != nil {
			tools.Reason = "Node was not found"
			return tools
		}
		tools.node, tools.Available = node, true
	}
	return tools
}

// PublishStatus is the progress of the publication of one instance.
type PublishStatus struct {
	State   string `json:"state"` // idle, running, done, failed
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

type publisher struct {
	mu          sync.Mutex
	jobs        map[string]*PublishStatus
	fingerprint map[string]cachedFingerprint
}

type cachedFingerprint struct {
	value string
	at    time.Time
}

func newPublisher() *publisher {
	return &publisher{jobs: map[string]*PublishStatus{}, fingerprint: map[string]cachedFingerprint{}}
}

func (p *publisher) status(alias string) PublishStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if job := p.jobs[alias]; job != nil {
		return *job
	}
	return PublishStatus{State: "idle"}
}

func (p *publisher) set(alias, step, state, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs[alias] = &PublishStatus{State: state, Step: step, Message: message}
}

// begin marks the job running unless one is already.
func (p *publisher) begin(alias string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if job := p.jobs[alias]; job != nil && job.State == "running" {
		return false
	}
	p.jobs[alias] = &PublishStatus{State: "running", Step: "preparation"}
	return true
}

func (p *publisher) currentFingerprint(rootPath string) (string, bool) {
	p.mu.Lock()
	cached, ok := p.fingerprint[rootPath]
	p.mu.Unlock()
	if ok && time.Since(cached.at) < fingerprintCacheTime {
		return cached.value, true
	}
	value, err := sourceFingerprint(rootPath)
	if err != nil {
		return "", false
	}
	p.mu.Lock()
	p.fingerprint[rootPath] = cachedFingerprint{value: value, at: time.Now()}
	p.mu.Unlock()
	return value, true
}

func (p *publisher) forget(rootPath string) {
	p.mu.Lock()
	delete(p.fingerprint, rootPath)
	p.mu.Unlock()
}

// publicationState tells how the instance's published web UIs relate to its sources, and which
// interfaces changed when the publication knows what each was built from.
func (ui *WebUI) publicationState(instance Instance) (string, []string) {
	if instance.PublicationID == "" {
		return "none", nil
	}
	record, ok := readPublicationRecord(ui.options.DataDirectory, instance.Alias)
	if !ok || record.PublicationID != instance.PublicationID {
		return "unknown", nil // associated by hand: the launcher does not know what it was built from
	}
	if len(record.Interfaces) > 0 {
		changed, err := changedInterfaces(instance.RootPath, record.Interfaces)
		if err != nil {
			return "unknown", nil
		}
		if len(changed) > 0 {
			return "obsolete", changed
		}
		return "current", nil
	}
	current, ok := ui.publisher.currentFingerprint(instance.RootPath)
	if !ok {
		return "unknown", nil
	}
	if current != record.Fingerprint {
		return "obsolete", nil
	}
	return "current", nil
}

// startPublish begins the publication of an instance in the background.
func (ui *WebUI) startPublish(instance Instance, force bool) error {
	tools := ui.options.Publish
	if !tools.Available {
		return errors.New(tools.Reason)
	}
	if state, _ := ui.publicationState(instance); state == "current" && !force {
		return errors.New("the publication is already up to date: no interface changed")
	}
	if !ui.publisher.begin(instance.Alias) {
		return errors.New("a publication is already running for this instance")
	}
	go ui.runPublish(instance, tools)
	return nil
}

func (ui *WebUI) runPublish(instance Instance, tools PublishTools) {
	alias := instance.Alias
	failed := func(step string, err error) { ui.publisher.set(alias, step, "failed", err.Error()) }

	webRoot, err := webRootFor(instance.RootPath)
	if err != nil {
		failed("preparation", err)
		return
	}
	fingerprint, err := sourceFingerprint(instance.RootPath)
	if err != nil {
		failed("preparation", err)
		return
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	output := filepath.Join(publicationFolder(ui.options.DataDirectory, alias), stamp)
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		failed("preparation", err)
		return
	}

	ui.publisher.set(alias, "build", "running", "")
	publicationID, interfaces, err := ui.buildPublication(tools, instance, output, webRoot)
	if err != nil {
		_ = os.RemoveAll(output)
		failed("build", err)
		return
	}

	// the MCP profiles the operator runs now: a restart stops their wrappers, and they are started again after it
	profiles := ui.runningMCPProfiles(alias)
	ui.publisher.set(alias, "stop", "running", "")
	wasRunning, err := ui.stopForPublication(alias)
	if err != nil {
		failed("stop", err)
		return
	}
	restart := func() {
		if wasRunning {
			if _, err := ui.call(LauncherControlRequest{Operation: "start", Alias: alias}); err == nil {
				ui.restoreMCPProfiles(alias, profiles)
			}
		}
	}

	if instance.RuntimePath == "" {
		ui.publisher.set(alias, "runtime", "running", "")
		if ui.options.Creation.runtime == "" {
			restart()
			failed("runtime", errors.New("this instance runs from a bare engine and no runtime package was built: run npm run setup"))
			return
		}
		if _, err := ui.call(LauncherControlRequest{Operation: "update_runtime", Alias: alias, RuntimePath: ui.options.Creation.runtime}); err != nil {
			restart()
			failed("runtime", err)
			return
		}
	}

	ui.publisher.set(alias, "associate", "running", "")
	if _, err := ui.call(LauncherControlRequest{Operation: "update_publication", Alias: alias, PublicationPath: output}); err != nil {
		restart()
		failed("associate", err)
		return
	}
	_ = writePublicationRecord(ui.options.DataDirectory, alias, PublicationRecord{PublicationID: publicationID,
		Fingerprint: fingerprint, Directory: output, At: time.Now().UTC().Format(time.RFC3339), Interfaces: interfaces})
	prunePublications(ui.options.DataDirectory, alias, output)
	ui.publisher.forget(instance.RootPath)

	if wasRunning {
		ui.publisher.set(alias, "start", "running", "")
		if _, err := ui.call(LauncherControlRequest{Operation: "start", Alias: alias}); err != nil {
			failed("start", fmt.Errorf("published, but the instance did not restart: %w", err))
			return
		}
		if len(profiles) > 0 {
			ui.publisher.set(alias, "mcp", "running", "")
			if missing := ui.restoreMCPProfiles(alias, profiles); len(missing) > 0 {
				failed("mcp", fmt.Errorf("published and restarted, but these MCP profiles did not start again: %s", strings.Join(missing, ", ")))
				return
			}
		}
	}
	ui.publisher.set(alias, "", "done", "")
}

// buildPublication runs the publication script and returns the publication id it reports.
func (ui *WebUI) buildPublication(tools PublishTools, instance Instance, output, webRoot string) (string, []InterfaceSnapshot, error) {
	args := []string{filepath.Join(tools.repository, "scripts", "publish-spark-web.mjs"),
		"--context", filepath.Join(instance.RootPath, "context.json"), "--output", output}
	if webRoot != "" {
		args = append(args, "--web-root", webRoot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, tools.node, args...)
	command.Dir = tools.repository
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := lastBytes(stderr.String(), 1500)
		if message == "" {
			message = lastBytes(stdout.String(), 1500)
		}
		return "", nil, fmt.Errorf("the publication script failed: %v: %s", err, message)
	}
	var report struct {
		PublicationID string `json:"publication_id"`
		Interfaces    []struct {
			ContextPath string   `json:"context_path"`
			UIName      string   `json:"ui_name"`
			Inputs      []string `json:"inputs"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.PublicationID == "" {
		return "", nil, errors.New("the publication script did not report a publication id")
	}
	snapshots := make([]InterfaceSnapshot, 0, len(report.Interfaces))
	for _, published := range report.Interfaces {
		snapshots = append(snapshots, snapshotInterface(published.ContextPath, published.UIName, published.Inputs))
	}
	return report.PublicationID, snapshots, nil
}

// stopForPublication stops the engine of the instance when it runs (associating a publication needs
// a stopped instance) and says whether it was running. An engine started outside the launcher is not
// the launcher's to stop.
// runningMCPProfiles names the MCP profiles the engine of the instance runs now; none when it does not run or does not answer.
func (ui *WebUI) runningMCPProfiles(alias string) []string {
	instance, address, err := ui.mcpTarget(alias)
	if err != nil {
		return nil
	}
	ui.engineMu.Lock()
	defer ui.engineMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	states, err := ReadMCPStates(ctx, instance, address)
	if err != nil {
		return nil
	}
	return runningProfiles(states)
}

// runningProfiles are the profiles whose wrapper runs or is starting.
func runningProfiles(states []MCPState) []string {
	var running []string
	for _, state := range states {
		if state.State == "running" || state.State == "starting" {
			running = append(running, state.Name)
		}
	}
	return running
}

// restoreMCPProfiles starts again, once the restarted instance is ready, the MCP profiles that ran before the restart: the operator
// had started them, and confirmed the architect profile, so the publication does not take them away. It returns those that did not start.
func (ui *WebUI) restoreMCPProfiles(alias string, profiles []string) []string {
	return restoreProfiles(profiles, func() (Instance, string, error) { return ui.mcpTarget(alias) }, &ui.engineMu, 60*time.Second)
}

// restoreProfiles waits, at most wait, for target to name a ready instance, then starts each profile.
func restoreProfiles(profiles []string, target func() (Instance, string, error), engine *sync.Mutex, wait time.Duration) []string {
	if len(profiles) == 0 {
		return nil
	}
	var instance Instance
	var address string
	deadline := time.Now().Add(wait)
	for {
		var err error
		if instance, address, err = target(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return profiles
		}
		time.Sleep(200 * time.Millisecond)
	}
	engine.Lock()
	defer engine.Unlock()
	var missing []string
	for _, name := range profiles {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := SetMCP(ctx, instance, address, name, "start", true); err != nil {
			missing = append(missing, name)
		}
		cancel()
	}
	return missing
}

func (ui *WebUI) stopForPublication(alias string) (bool, error) {
	response, err := ui.call(LauncherControlRequest{Operation: "status", Alias: alias})
	if err != nil || response.Status == nil {
		return false, errors.New("the supervisor does not answer")
	}
	if response.Status.State == StateStopped || response.Status.State == StateFailed || response.Status.State == StateInterrupted {
		return false, nil
	}
	if !response.Status.Owned {
		return false, errors.New("the engine was started outside the launcher: stop it where it was started, then publish")
	}
	if _, err := ui.call(LauncherControlRequest{Operation: "stop", Alias: alias}); err != nil {
		return false, err
	}
	return true, nil
}

func lastBytes(value string, limit int) string {
	if len(value) > limit {
		return value[len(value)-limit:]
	}
	return value
}

// useRuntime switches an instance that runs from a bare engine to the runtime package the setup
// built, which an instance needs to serve a web page or to carry the browser extension's native host.
// A running engine owned by the launcher is stopped for the change and started again.
func (ui *WebUI) useRuntime(alias string) error {
	instance, err := ui.options.Store.Get(alias)
	if err != nil {
		return err
	}
	if instance.RuntimePath != "" {
		return nil
	}
	if ui.options.Creation.runtime == "" {
		return errors.New("no runtime package: run npm run setup")
	}
	wasRunning, err := ui.stopForPublication(alias)
	if err != nil {
		return err
	}
	if _, err := ui.call(LauncherControlRequest{Operation: "update_runtime", Alias: alias, RuntimePath: ui.options.Creation.runtime}); err != nil {
		if wasRunning {
			_, _ = ui.call(LauncherControlRequest{Operation: "start", Alias: alias})
		}
		return err
	}
	if wasRunning {
		if _, err := ui.call(LauncherControlRequest{Operation: "start", Alias: alias}); err != nil {
			return fmt.Errorf("the package was applied, but the instance did not restart: %w", err)
		}
	}
	return nil
}
