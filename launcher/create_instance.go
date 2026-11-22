/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const templateInstanceName = "my-brique-instance" // the name the template carries; init refuses it for a real instance

var instanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// CreateInstanceOptions describes a new instance made from the template of the repository.
type CreateInstanceOptions struct {
	// Name is both the alias in the registry and the identifier of the instance.
	Name string
	// ParentDirectory is an absolute folder; the instance is created in <ParentDirectory>/<Name>.
	ParentDirectory   string
	TemplateDirectory string // the instance template (templates/instance of the repository)
	SecurityBinary    string // brique-sec
	// RuntimePath is the runtime package the instance runs from (preferred: only a package can serve
	// a web page); EnginePath is the bare engine used when there is no package.
	RuntimePath string
	EnginePath  string
}

// CreationDefaults says whether this launcher can create instances (it is run from a built
// repository) and with which tools.
type CreationDefaults struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Parent    string `json:"parent"`

	template, security, engine, runtime string
}

// DetectCreationDefaults finds the template, brique-sec and the engine next to the launcher
// binary: bin/ and templates/ of the repository that `npm run setup` built.
func DetectCreationDefaults(executable string) CreationDefaults {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binDirectory := filepath.Dir(executable)
	result := CreationDefaults{
		template: filepath.Join(binDirectory, "..", "templates", "instance"),
		security: filepath.Join(binDirectory, "brique-sec"+suffix),
		engine:   filepath.Join(binDirectory, "brique"+suffix),
	}
	result.runtime = currentRuntimePackage(filepath.Join(binDirectory, ".."))
	if home, err := os.UserHomeDir(); err == nil {
		result.Parent = filepath.Join(home, "Brique")
	}
	switch {
	case !isDirectory(result.template):
		result.Reason = "the instance template (templates/instance) was not found next to the launcher"
	case !isFile(result.security) || (!isFile(result.engine) && result.runtime == ""):
		result.Reason = "brique and brique-sec are not built: run npm run setup"
	case result.Parent == "":
		result.Reason = "home directory not found"
	default:
		result.Available = true
	}
	return result
}

func isDirectory(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// CreateInstance copies the template to a new folder, gives the instance its own security
// (brique-sec init --solo) and registers it. A folder that already exists is never reused.
// When a step fails after the folder was created, that folder is removed again.
func (s Store) CreateInstance(ctx context.Context, options CreateInstanceOptions) (Instance, error) {
	name := options.Name
	if !instanceNamePattern.MatchString(name) || !validAlias(name) || name == templateInstanceName {
		return Instance{}, errors.New("instance name: lowercase letters, digits, '.', '_' or '-', starting with a letter")
	}
	if !filepath.IsAbs(options.ParentDirectory) {
		return Instance{}, errors.New("the parent folder must be an absolute path")
	}
	if !isDirectory(options.TemplateDirectory) || !isFile(options.SecurityBinary) ||
		(options.RuntimePath == "" && !isFile(options.EnginePath)) || (options.RuntimePath != "" && !isDirectory(options.RuntimePath)) {
		return Instance{}, errors.New("template, brique-sec, or engine/runtime package not found")
	}
	if err := os.MkdirAll(options.ParentDirectory, 0o755); err != nil {
		return Instance{}, err
	}
	target := filepath.Join(options.ParentDirectory, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Instance{}, fmt.Errorf("%s already exists", target)
		}
		return Instance{}, err
	}
	instance, err := s.populateInstance(ctx, target, options)
	if err != nil {
		_ = os.RemoveAll(target)
		return Instance{}, err
	}
	return instance, nil
}

func (s Store) populateInstance(ctx context.Context, target string, options CreateInstanceOptions) (Instance, error) {
	if err := copyTemplate(options.TemplateDirectory, target); err != nil {
		return Instance{}, fmt.Errorf("copy the template: %w", err)
	}
	root := filepath.Join(target, "root")
	runContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// The instance starts with the browser of its owner admitted as local.ui, holding every right on the
	// instance: on a machine one person controls, the owner's own browser is the owner. The signed policy is
	// applied once so that the first start already carries it. Narrower rights are a later, explicit choice.
	steps := [][]string{{"init", "--instance", options.Name, "--solo"}, {"principal", "add", "local.ui", "--kind", "local"}}
	for _, verb := range []string{"read", "write", "create", "call", "control", "define"} {
		steps = append(steps, []string{"grant", "add", "local.ui", verb, "/root/**"})
	}
	steps = append(steps, []string{"apply"})
	for _, step := range steps {
		command := exec.CommandContext(runContext, options.SecurityBinary, append([]string{"--root", root}, step...)...)
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		if err := command.Run(); err != nil {
			message := strings.TrimSpace(output.String())
			if len(message) > 2000 {
				message = message[len(message)-2000:]
			}
			return Instance{}, fmt.Errorf("brique-sec %s failed: %v: %s", step[0], err, message)
		}
	}
	return s.Add(ctx, AddOptions{Alias: options.Name, RootPath: root, RuntimePath: options.RuntimePath,
		EnginePath: runtimeFreeEngine(options), Supervision: SupervisionManaged})
}

// copyTemplate copies regular files and folders; it skips dependency folders and refuses links,
// which a template has no reason to carry.
func copyTemplate(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		if entry.Name() == "node_modules" || entry.Name() == ".DS_Store" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		destination := filepath.Join(target, relative)
		switch {
		case entry.IsDir():
			return os.MkdirAll(destination, 0o755)
		case entry.Type().IsRegular():
			return copyTemplateFile(path, destination)
		default:
			return fmt.Errorf("%s is not a regular file", relative)
		}
	})
}

func copyTemplateFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// runtimeFreeEngine is the bare engine, only when no runtime package is used (the two are exclusive).
func runtimeFreeEngine(options CreateInstanceOptions) string {
	if options.RuntimePath != "" {
		return ""
	}
	return options.EnginePath
}

var runtimeNamePattern = regexp.MustCompile(`^[0-9a-z]+$`)

// currentRuntimePackage returns the runtime package `npm run setup` last built (named in
// <repository>/runtime/CURRENT), or "" when there is none or it is not a package.
func currentRuntimePackage(repository string) string {
	raw, err := os.ReadFile(filepath.Join(repository, "runtime", "CURRENT"))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(raw))
	if !runtimeNamePattern.MatchString(name) {
		return ""
	}
	directory := filepath.Join(repository, "runtime", name)
	if !isFile(filepath.Join(directory, RuntimeManifestName)) {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(directory); err == nil {
		return resolved
	}
	return directory
}

// PreparePythonEnvironments builds the Python environments of an instance: one for each folder envs/<name>/ that has a requirements.txt
// (the engine runs a wrapper under envs/<name>/venv and never makes it). It is the one step of a creation that needs the network and may
// fail, so it does not fail the creation: what went wrong comes back as a sentence the person can act on, and the instance still starts
// (the wrappers that need nothing from the environment, the MCP and the web, do not depend on it).
func PreparePythonEnvironments(ctx context.Context, instanceDirectory string) []string {
	entries, err := os.ReadDir(filepath.Join(instanceDirectory, "envs"))
	if err != nil {
		return nil
	}
	var warnings []string
	for _, entry := range entries {
		directory := filepath.Join(instanceDirectory, "envs", entry.Name())
		requirements := filepath.Join(directory, "requirements.txt")
		if !entry.IsDir() || !isFile(requirements) {
			continue
		}
		if err := buildPythonEnvironment(ctx, directory, requirements); err != nil {
			warnings = append(warnings, fmt.Sprintf("Python environment %q was not built (%v). The wrappers written in Python need it: run python3 -m venv %s, then %s install -r %s",
				entry.Name(), err, filepath.Join(directory, "venv"), filepath.Join(directory, "venv", pythonVenvBin(), "pip"), requirements))
		}
	}
	return warnings
}

func pythonVenvBin() string {
	if runtime.GOOS == "windows" {
		return "Scripts"
	}
	return "bin"
}

func buildPythonEnvironment(ctx context.Context, directory, requirements string) error {
	python, err := exec.LookPath("python3")
	if err != nil {
		if python, err = exec.LookPath("python"); err != nil {
			return errors.New("python3 was not found")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	venv := filepath.Join(directory, "venv")
	for _, step := range [][]string{{"-m", "venv", venv}, {filepath.Join(venv, pythonVenvBin(), "python"), "-m", "pip", "install", "--disable-pip-version-check", "-q", "-r", requirements}} {
		name, args := python, step
		if len(step) > 0 && filepath.IsAbs(step[0]) {
			name, args = step[0], step[1:]
		}
		command := exec.CommandContext(ctx, name, args...)
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		if err := command.Run(); err != nil {
			message := strings.TrimSpace(output.String())
			if len(message) > 400 {
				message = message[len(message)-400:]
			}
			_ = os.RemoveAll(venv)
			return fmt.Errorf("%v: %s", err, message)
		}
	}
	return nil
}
