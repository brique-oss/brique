/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"brique_engine/platform/protectedfile"
)

const (
	BrowserGatewayFormat   = "brique.browser-gateway/1"
	browserGatewayRelative = "browser/gateway.json"
	browserGatewayLock     = "browser/gateway.lock"
	maxBrowserGatewayBytes = 16 << 10
)

var ErrBrowserGatewayUnavailable = errors.New("browser gateway is not configured")

// BrowserGateway is the machine-local choice made by the user. It contains no
// Brique identity or browser-provided input: a page is never allowed to select
// which local instance will act for it.
type BrowserGateway struct {
	Format   string `json:"format"`
	Revision uint64 `json:"revision"`
	Alias    string `json:"alias,omitempty"`
}

func (s Store) SetBrowserGateway(alias string) (BrowserGateway, error) {
	if !validAlias(alias) {
		return BrowserGateway{}, errors.New("invalid instance alias")
	}
	if _, err := s.Get(alias); err != nil {
		return BrowserGateway{}, err
	}
	return s.updateBrowserGateway(alias)
}

// ClearBrowserGateway records an explicit empty choice instead of deleting a
// file. Readers therefore observe one atomic revision transition.
func (s Store) ClearBrowserGateway() (BrowserGateway, error) {
	return s.updateBrowserGateway("")
}

func (s Store) BrowserGateway() (BrowserGateway, error) {
	directory, err := s.browserDirectory()
	if err != nil {
		return BrowserGateway{}, err
	}
	gateway, err := s.readBrowserGateway(directory)
	if err != nil {
		return BrowserGateway{}, err
	}
	if gateway.Alias == "" {
		return BrowserGateway{}, ErrBrowserGatewayUnavailable
	}
	if _, err := s.Get(gateway.Alias); err != nil {
		return BrowserGateway{}, ErrBrowserGatewayUnavailable
	}
	return gateway, nil
}

func (s Store) updateBrowserGateway(alias string) (BrowserGateway, error) {
	directory, err := s.browserDirectory()
	if err != nil {
		return BrowserGateway{}, err
	}
	lock, err := protectedfile.OpenProtectedFile(filepath.Join(directory, filepath.FromSlash(browserGatewayLock)), true, nil, nil)
	if err != nil {
		return BrowserGateway{}, err
	}
	defer lock.Close()
	locked, err := protectedfile.LockFile(lock, true)
	if err != nil || !locked {
		return BrowserGateway{}, errors.New("browser gateway lock unavailable")
	}
	defer protectedfile.UnlockFile(lock)

	current, err := s.readBrowserGateway(directory)
	if errors.Is(err, ErrBrowserGatewayUnavailable) {
		current = BrowserGateway{Format: BrowserGatewayFormat}
	} else if err != nil {
		return BrowserGateway{}, err
	}
	next := BrowserGateway{Format: BrowserGatewayFormat, Revision: current.Revision + 1, Alias: alias}
	raw, err := json.Marshal(next)
	if err != nil {
		return BrowserGateway{}, errors.New("encode browser gateway")
	}
	if err := protectedfile.WriteProtectedFile(directory, browserGatewayRelative, append(raw, '\n'), 0o600); err != nil {
		return BrowserGateway{}, err
	}
	return next, nil
}

func (s Store) browserDirectory() (string, error) {
	if s.Directory == "" {
		return "", errors.New("launcher data directory is required")
	}
	directory, err := filepath.Abs(s.Directory)
	if err != nil {
		return "", errors.New("invalid launcher data directory")
	}
	if err := protectedfile.CreateProtectedDirectory(directory); err != nil {
		return "", err
	}
	if err := protectedfile.CreateProtectedDirectory(filepath.Join(directory, "browser")); err != nil {
		return "", err
	}
	if err := protectedfile.ValidateProtectedDirectory(directory, nil); err != nil {
		return "", err
	}
	return directory, nil
}

func (s Store) readBrowserGateway(directory string) (BrowserGateway, error) {
	path := filepath.Join(directory, filepath.FromSlash(browserGatewayRelative))
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return BrowserGateway{}, ErrBrowserGatewayUnavailable
	} else if err != nil {
		return BrowserGateway{}, errors.New("browser gateway unavailable")
	}
	raw, err := protectedfile.ReadProtectedFile(directory, browserGatewayRelative, protectedfile.ProtectedFileOptions{MaxBytes: maxBrowserGatewayBytes})
	if err != nil {
		return BrowserGateway{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var gateway BrowserGateway
	if err := decoder.Decode(&gateway); err != nil {
		return BrowserGateway{}, errors.New("invalid browser gateway")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return BrowserGateway{}, errors.New("invalid trailing browser gateway data")
	}
	if gateway.Format != BrowserGatewayFormat || gateway.Revision == 0 || (gateway.Alias != "" && !validAlias(gateway.Alias)) {
		return BrowserGateway{}, errors.New("invalid browser gateway")
	}
	return gateway, nil
}
