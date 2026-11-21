/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package localruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"brique_engine/browserbridge"
	"brique_engine/shared"
)

const (
	EngineRuntimeFormatV1 = "brique.engine-runtime/1"
	EngineRuntimeFormat   = "brique.engine-runtime/2"
	LocalControlProtocol  = "brique.local-control/1"
	maxDescriptorBytes    = 64 << 10

	StateStarting = "starting"
	StateReady    = "ready"
	StateStopping = "stopping"
	StateFailed   = "failed"

	TransportUnixSocket       = "unix"
	TransportWindowsNamedPipe = "windows_named_pipe"
)

type ControlEndpoint struct {
	Protocol  string `json:"protocol"`
	Transport string `json:"transport"`
	Address   string `json:"address"`
}

type EngineRuntimeDescriptor struct {
	Format          string                           `json:"format"`
	DiscoveryKey    string                           `json:"discovery_key"`
	RootPath        string                           `json:"root_path"`
	RootObject      shared.DirectoryObjectID         `json:"root_object"`
	Build           shared.BuildInfo                 `json:"build"`
	PID             int                              `json:"pid"`
	Generation      string                           `json:"generation"`
	State           string                           `json:"state"`
	Control         ControlEndpoint                  `json:"control"`
	BrowserBridge   *browserbridge.EngineIPCEndpoint `json:"browser_bridge,omitempty"`
	UIWebSocket     string                           `json:"ui_websocket_address,omitempty"`
	PublicWebSocket string                           `json:"public_websocket_address,omitempty"`
	PublishedAt     string                           `json:"published_at"`
}

func (descriptor EngineRuntimeDescriptor) Validate() error {
	if (descriptor.Format != EngineRuntimeFormatV1 && descriptor.Format != EngineRuntimeFormat) || !descriptor.RootObject.Valid() || !descriptor.Build.Valid() || descriptor.PID < 1 || !ValidGeneration(descriptor.Generation) {
		return errors.New("invalid engine runtime descriptor identity")
	}
	if descriptor.Format == EngineRuntimeFormatV1 && descriptor.PublicWebSocket != "" {
		return errors.New("public web socket requires engine runtime descriptor format 2")
	}
	if !filepath.IsAbs(descriptor.RootPath) || filepath.Clean(descriptor.RootPath) != descriptor.RootPath {
		return errors.New("engine runtime root path is not canonical and absolute")
	}
	expectedKey, err := DiscoveryKey(descriptor.RootObject)
	if err != nil || descriptor.DiscoveryKey != expectedKey {
		return errors.New("engine runtime discovery key does not match its root object")
	}
	switch descriptor.State {
	case StateStarting, StateReady, StateStopping, StateFailed:
	default:
		return errors.New("invalid engine runtime state")
	}
	if err := descriptor.Control.validate(); err != nil {
		return err
	}
	if descriptor.BrowserBridge != nil {
		if err := descriptor.BrowserBridge.Validate(); err != nil {
			return errors.New("invalid browser bridge endpoint")
		}
	}
	if descriptor.UIWebSocket != "" {
		host, _, err := net.SplitHostPort(descriptor.UIWebSocket)
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if err != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("invalid local UI websocket address")
		}
	}
	if descriptor.PublicWebSocket != "" {
		endpoint, err := url.Parse(descriptor.PublicWebSocket)
		host, _, splitErr := net.SplitHostPort(endpoint.Host)
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if err != nil || splitErr != nil || endpoint.Scheme != "ws" || ip == nil || !ip.IsLoopback() ||
			endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/__brique/public/" {
			return errors.New("invalid public web socket address")
		}
	}
	published, err := time.Parse(time.RFC3339Nano, descriptor.PublishedAt)
	if err != nil || published.Location() != time.UTC {
		return errors.New("invalid engine runtime publication time")
	}
	return nil
}

func (endpoint ControlEndpoint) validate() error {
	if endpoint.Protocol != LocalControlProtocol || endpoint.Address == "" || len(endpoint.Address) > 512 {
		return errors.New("invalid local control endpoint")
	}
	switch endpoint.Transport {
	case TransportUnixSocket:
		if !filepath.IsAbs(endpoint.Address) || filepath.Clean(endpoint.Address) != endpoint.Address {
			return errors.New("invalid unix control endpoint")
		}
	case TransportWindowsNamedPipe:
		if !strings.HasPrefix(strings.ToLower(endpoint.Address), `\\.\pipe\brique-`) {
			return errors.New("invalid windows control endpoint")
		}
	default:
		return errors.New("unknown local control transport")
	}
	return nil
}

func NewDescriptor(rootPath string, rootObject shared.DirectoryObjectID, generation, state string, endpoint ControlEndpoint, now time.Time) (EngineRuntimeDescriptor, error) {
	discoveryKey, err := DiscoveryKey(rootObject)
	if err != nil {
		return EngineRuntimeDescriptor{}, err
	}
	descriptor := EngineRuntimeDescriptor{
		Format: EngineRuntimeFormat, DiscoveryKey: discoveryKey,
		RootPath: filepath.Clean(rootPath), RootObject: rootObject,
		Build: shared.CurrentBuildInfo(), PID: os.Getpid(), Generation: generation,
		State: state, Control: endpoint, PublishedAt: now.UTC().Format(time.RFC3339Nano),
	}
	return descriptor, descriptor.Validate()
}

func decodeDescriptor(raw []byte) (EngineRuntimeDescriptor, error) {
	if len(raw) == 0 || len(raw) > maxDescriptorBytes {
		return EngineRuntimeDescriptor{}, errors.New("invalid engine runtime descriptor size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var descriptor EngineRuntimeDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return EngineRuntimeDescriptor{}, errors.New("invalid engine runtime descriptor")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return EngineRuntimeDescriptor{}, errors.New("invalid trailing engine runtime descriptor data")
	}
	if err := descriptor.Validate(); err != nil {
		return EngineRuntimeDescriptor{}, err
	}
	return descriptor, nil
}

func descriptorRelativePath(discoveryKey string) (string, error) {
	if !ValidDiscoveryKey(discoveryKey) {
		return "", errors.New("invalid discovery key")
	}
	return "instances/" + discoveryKey + ".json", nil
}

func PrepareDiscoveryDirectory(directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("discovery directory must be absolute")
	}
	if err := shared.CreateProtectedDirectory(directory); err != nil {
		return fmt.Errorf("create discovery directory: %w", err)
	}
	if err := shared.ValidateProtectedDirectory(directory, nil); err != nil {
		return fmt.Errorf("validate discovery directory: %w", err)
	}
	return nil
}

func PublishDescriptor(directory string, descriptor EngineRuntimeDescriptor) error {
	if err := descriptor.Validate(); err != nil {
		return err
	}
	if err := PrepareDiscoveryDirectory(directory); err != nil {
		return err
	}
	relative, _ := descriptorRelativePath(descriptor.DiscoveryKey)
	raw, err := json.Marshal(descriptor)
	if err != nil || len(raw) > maxDescriptorBytes-1 {
		return errors.New("encode engine runtime descriptor")
	}
	raw = append(raw, '\n')
	return shared.WriteProtectedFile(directory, relative, raw, 0o600)
}

func LoadDescriptor(directory, discoveryKey string) (EngineRuntimeDescriptor, error) {
	relative, err := descriptorRelativePath(discoveryKey)
	if err != nil {
		return EngineRuntimeDescriptor{}, err
	}
	raw, err := shared.ReadProtectedFile(directory, relative, shared.ProtectedFileOptions{MaxBytes: maxDescriptorBytes})
	if err != nil {
		return EngineRuntimeDescriptor{}, errors.New("engine runtime descriptor unavailable")
	}
	return decodeDescriptor(raw)
}

var ErrDescriptorNotOwned = errors.New("engine runtime descriptor belongs to another generation")

// RemoveOwnedDescriptor is called while the engine still owns the root object
// lock, so another valid generation cannot be published between load and remove.
func RemoveOwnedDescriptor(directory, discoveryKey, generation string) error {
	descriptor, err := LoadDescriptor(directory, discoveryKey)
	if err != nil {
		return err
	}
	if descriptor.Generation != generation {
		return ErrDescriptorNotOwned
	}
	relative, _ := descriptorRelativePath(discoveryKey)
	if err := os.Remove(filepath.Join(directory, filepath.FromSlash(relative))); err != nil {
		return errors.New("remove engine runtime descriptor")
	}
	return nil
}
