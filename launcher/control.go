/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/platform/listener"
	"brique_engine/platform/protectedfile"
)

const (
	LauncherControlFormat = "brique.launcher-control/1"
	maxLauncherControl    = 64 << 10
	launcherControlWait   = 30 * time.Second
)

type LauncherControlRequest struct {
	Format          string      `json:"format"`
	Operation       string      `json:"operation"`
	Alias           string      `json:"alias,omitempty"`
	EnginePath      string      `json:"engine_path,omitempty"`
	RuntimePath     string      `json:"runtime_path,omitempty"`
	PublicationPath string      `json:"publication_path,omitempty"`
	WebProfile      *WebProfile `json:"web_profile,omitempty"`
	// Peer and Endpoint name a declared peer (and one of its endpoints) for diagnose_connection.
	Peer     string `json:"peer,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

type LauncherControlResponse struct {
	Format    string                      `json:"format"`
	OK        bool                        `json:"ok"`
	Error     string                      `json:"error,omitempty"`
	Status    *Status                     `json:"status,omitempty"`
	Statuses  []Status                    `json:"statuses,omitempty"`
	Instance  *Instance                   `json:"instance,omitempty"`
	Diagnosis *connectiondiagnosis.Result `json:"diagnosis,omitempty"`
}

type LauncherControlServer struct {
	supervisor   *Supervisor
	listener     net.Listener
	cleanup      func() error
	lock         *os.File
	closed       chan struct{}
	shutdown     chan struct{}
	closeOnce    sync.Once
	shutdownOnce sync.Once
	wg           sync.WaitGroup
	sem          chan struct{}
}

func StartLauncherControl(supervisor *Supervisor, controlRuntimeDirectory string) (*LauncherControlServer, error) {
	if supervisor == nil {
		return nil, errors.New("supervisor is required")
	}
	if _, err := supervisor.store.List(); err != nil {
		return nil, err
	}
	dataDirectory, key, err := launcherControlKey(supervisor.store.Directory)
	if err != nil {
		return nil, err
	}
	lock, err := protectedfile.OpenProtectedFile(filepath.Join(dataDirectory, "registry", "supervisor.lock"), true, nil, nil)
	if err != nil {
		return nil, err
	}
	locked, err := protectedfile.LockFile(lock, false)
	if err != nil || !locked {
		_ = lock.Close()
		return nil, errors.New("another launcher supervisor owns this registry")
	}
	listener, cleanup, err := listenLauncherControl(controlRuntimeDirectory, key)
	if err != nil {
		_ = protectedfile.UnlockFile(lock)
		_ = lock.Close()
		return nil, err
	}
	server := &LauncherControlServer{supervisor: supervisor, listener: listener, cleanup: cleanup, lock: lock,
		closed: make(chan struct{}), shutdown: make(chan struct{}), sem: make(chan struct{}, 8)}
	server.wg.Add(1)
	go server.acceptLoop()
	return server, nil
}

func (server *LauncherControlServer) ShutdownRequested() <-chan struct{} { return server.shutdown }

func (server *LauncherControlServer) Close() error {
	var result error
	server.closeOnce.Do(func() {
		close(server.closed)
		if err := server.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = err
		}
		server.wg.Wait()
		if err := server.cleanup(); err != nil && result == nil {
			result = err
		}
		_ = protectedfile.UnlockFile(server.lock)
		if err := server.lock.Close(); err != nil && result == nil {
			result = err
		}
	})
	return result
}

func (server *LauncherControlServer) acceptLoop() {
	defer server.wg.Done()
	backoff := listener.AcceptBackoff{Name: "launcher control"}
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if !backoff.Failed(err, server.closed) {
				return
			}
			continue
		}
		backoff.Succeeded()
		select {
		case server.sem <- struct{}{}:
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				defer func() { <-server.sem }()
				server.handle(connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (server *LauncherControlServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(launcherControlWait))
	reader := bufio.NewReaderSize(connection, maxLauncherControl+1)
	raw, err := reader.ReadSlice('\n')
	if err != nil || len(raw) > maxLauncherControl {
		return
	}
	request, err := decodeLauncherControlRequest(raw)
	if err != nil {
		_ = writeLauncherControlResponse(connection, LauncherControlResponse{Format: LauncherControlFormat, Error: "invalid request"})
		return
	}
	response := LauncherControlResponse{Format: LauncherControlFormat}
	ctx, cancel := context.WithTimeout(context.Background(), launcherControlWait-2*time.Second)
	defer cancel()
	switch request.Operation {
	case "list":
		response.Statuses, err = server.supervisor.Statuses(ctx)
	case "status":
		var status Status
		status, err = server.supervisor.Status(ctx, request.Alias)
		response.Status = &status
	case "start":
		var status Status
		status, err = server.supervisor.Start(ctx, request.Alias)
		response.Status = &status
	case "stop":
		var status Status
		status, err = server.supervisor.Stop(ctx, request.Alias)
		response.Status = &status
	case "restart":
		var status Status
		status, err = server.supervisor.Restart(ctx, request.Alias)
		response.Status = &status
	case "diagnose_connection":
		var diagnosis connectiondiagnosis.Result
		diagnosis, err = server.supervisor.DiagnoseConnection(ctx, request.Alias, request.Peer, request.Endpoint)
		if err == nil {
			response.Diagnosis = &diagnosis
		}
	case "update_engine":
		var instance Instance
		instance, err = server.supervisor.UpdateEngine(ctx, request.Alias, request.EnginePath)
		response.Instance = &instance
	case "update_runtime":
		var instance Instance
		instance, err = server.supervisor.UpdateRuntime(ctx, request.Alias, request.RuntimePath)
		response.Instance = &instance
	case "update_publication":
		var instance Instance
		instance, err = server.supervisor.UpdatePublication(ctx, request.Alias, request.PublicationPath)
		response.Instance = &instance
	case "update_web_profile":
		var instance Instance
		instance, err = server.supervisor.UpdateWebProfile(ctx, request.Alias, request.WebProfile)
		response.Instance = &instance
	case "shutdown":
		server.shutdownOnce.Do(func() { close(server.shutdown) })
	default:
		err = errors.New("unknown operation")
	}
	response.OK = err == nil
	if err != nil {
		response.Error = err.Error()
	}
	_ = writeLauncherControlResponse(connection, response)
}

func CallLauncherControl(dataDirectory, runtimeDirectory string, request LauncherControlRequest) (LauncherControlResponse, error) {
	_, key, err := launcherControlKey(dataDirectory)
	if err != nil {
		return LauncherControlResponse{}, err
	}
	runtimeDirectory, err = resolveControlRuntimeDirectory(runtimeDirectory)
	if err != nil {
		return LauncherControlResponse{}, err
	}
	connection, err := dialLauncherControl(runtimeDirectory, key, 5*time.Second)
	if err != nil {
		return LauncherControlResponse{}, errors.New("launcher supervisor is unavailable")
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(launcherControlWait))
	request.Format = LauncherControlFormat
	raw, err := json.Marshal(request)
	if err != nil || len(raw) >= maxLauncherControl {
		return LauncherControlResponse{}, errors.New("invalid launcher control request")
	}
	if _, err := connection.Write(append(raw, '\n')); err != nil {
		return LauncherControlResponse{}, err
	}
	reader := bufio.NewReaderSize(connection, maxLauncherControl+1)
	responseRaw, err := reader.ReadSlice('\n')
	if err != nil || len(responseRaw) > maxLauncherControl {
		return LauncherControlResponse{}, errors.New("invalid launcher control response")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseRaw))
	decoder.DisallowUnknownFields()
	var response LauncherControlResponse
	if err := decoder.Decode(&response); err != nil || decoder.Decode(&struct{}{}) != io.EOF || response.Format != LauncherControlFormat {
		return LauncherControlResponse{}, errors.New("invalid launcher control response")
	}
	if !response.OK {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func decodeLauncherControlRequest(raw []byte) (LauncherControlRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request LauncherControlRequest
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || request.Format != LauncherControlFormat {
		return LauncherControlRequest{}, errors.New("invalid request")
	}
	switch request.Operation {
	case "diagnose_connection":
		if !validAlias(request.Alias) || !validDiagnosisName(request.Peer) || request.Endpoint != "" && !validDiagnosisName(request.Endpoint) ||
			request.EnginePath != "" || request.RuntimePath != "" || request.PublicationPath != "" || request.WebProfile != nil {
			return LauncherControlRequest{}, errors.New("alias and peer required")
		}
	case "list", "shutdown":
		if request.Peer != "" || request.Endpoint != "" || request.Alias != "" || request.EnginePath != "" || request.RuntimePath != "" || request.PublicationPath != "" || request.WebProfile != nil {
			return LauncherControlRequest{}, errors.New("arguments not allowed")
		}
	case "status", "start", "stop", "restart":
		if request.Peer != "" || request.Endpoint != "" || !validAlias(request.Alias) || request.EnginePath != "" || request.RuntimePath != "" || request.PublicationPath != "" || request.WebProfile != nil {
			return LauncherControlRequest{}, errors.New("alias required")
		}
	case "update_engine":
		if !validAlias(request.Alias) || request.EnginePath == "" || request.RuntimePath != "" || request.PublicationPath != "" || request.WebProfile != nil {
			return LauncherControlRequest{}, errors.New("alias and engine path required")
		}
	case "update_runtime":
		if !validAlias(request.Alias) || request.RuntimePath == "" || request.EnginePath != "" || request.PublicationPath != "" || request.WebProfile != nil {
			return LauncherControlRequest{}, errors.New("alias and runtime path required")
		}
	case "update_publication":
		if !validAlias(request.Alias) || request.EnginePath != "" || request.RuntimePath != "" || request.WebProfile != nil {
			return LauncherControlRequest{}, errors.New("alias required")
		}
	case "update_web_profile":
		if !validAlias(request.Alias) || request.EnginePath != "" || request.RuntimePath != "" || request.PublicationPath != "" {
			return LauncherControlRequest{}, errors.New("alias required")
		}
	default:
		return LauncherControlRequest{}, errors.New("unknown operation")
	}
	return request, nil
}

// validDiagnosisName accepts a peer or endpoint name: printable ASCII without separators
// that make it look like an address or a path. The engine applies the same rule.
func validDiagnosisName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e || r == '/' || r == '\\' || r == ':' {
			return false
		}
	}
	return true
}

func writeLauncherControlResponse(writer io.Writer, response LauncherControlResponse) error {
	raw, err := json.Marshal(response)
	if err != nil || len(raw) >= maxLauncherControl {
		return errors.New("encode launcher control response")
	}
	_, err = writer.Write(append(raw, '\n'))
	return err
}

func launcherControlKey(directory string) (string, string, error) {
	if directory == "" {
		return "", "", errors.New("launcher data directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	key := base64.RawURLEncoding.EncodeToString(digest[:18])
	return canonical, key, nil
}

func resolveControlRuntimeDirectory(directory string) (string, error) {
	// Kept here to avoid exposing listener details through localruntime.
	if directory == "" {
		return defaultLauncherRuntimeDirectory()
	}
	if !filepath.IsAbs(directory) {
		return "", errors.New("launcher runtime directory must be absolute")
	}
	return filepath.Clean(directory), nil
}
