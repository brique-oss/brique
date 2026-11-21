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

package matter

// matter/https_substance_getter.go
//
// SubstanceHTTP (data-plane) for brique-mode substance bytes.
//
// Goals:
// - Start/Stop an HTTP server as part of MatterLoop lifecycle.
// - Create read/write "leases" bound to (rid, tok, matter_id, rev, expiry).
// - Serve bytes from disk for READ leases.
// - Receive bytes to disk for WRITE leases (data-plane ONLY).
//   Finalization (atomic replace + metadata commit + catalog update) is delegated to
//   a control-plane function associated with matter.write.
// - Close lease when transfer completes (or expires).
// - Prevent TOCTOU by binding lease to rev.
//
// Non-goals:
// - Global auth / identity. Control-plane already authorized; data-plane checks only tok+expiry+binding.
// - Resumable uploads and multipart transfers.

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
)

const substancePathPrefix = "/substance/"
const healthzPath = "/healthz"

const defaultMaxUploadBytes = int64(256 << 20) // 256MB
const defaultUploadIdleTimeout = 15 * time.Second
const defaultUploadMaxDuration = 10 * time.Minute
const defaultMaxTransfers = 32
const defaultLeaseMaxLife = 10 * time.Minute
const defaultAuthorizationRecheck = 5 * time.Second
const defaultReadIdleTimeout = 15 * time.Second

const timeToLiveLease = 30 //30s
const sweeperPeriod = 1    //1s

// -----------------------------
// Public handle returned by matter.read / matter.write
// -----------------------------

type SubstanceHTTPHandle struct {
	Kind      string         `json:"kind"` // "http"
	BaseURL   string         `json:"base_url"`
	Path      string         `json:"path"` // "/substance/<rid>"
	RID       string         `json:"rid"`
	Tok       string         `json:"tok"`
	ExpiresAt int64          `json:"expires_at"` // unix_nano
	Rev       int64          `json:"revision"`   // lease rev binding
	NewBrique map[string]any `json:"new_brique"`
	SizeHint  int64          `json:"size_hint,omitempty"`
	Method    string         `json:"method"` // "GET" or "PUT"
}

// -----------------------------
// Internal lease model
// -----------------------------

type leaseMode string

const (
	leaseRead   leaseMode = "read"
	leaseWrite  leaseMode = "write"
	leaseAppend leaseMode = "append"
)

type lease struct {
	owner      *MatterLoop
	rid        string
	tok        string
	mode       leaseMode
	matter     string
	rev        int64
	newBrique  map[string]any
	expires    time.Time
	maxExpires time.Time

	// Control-plane provenance (best-effort) for notifications.
	srcIntentionID string

	// Disk binding (brique-mode only)
	ctxDir        string
	matterRoot    string
	substancePath string

	// For WRITE
	tmpSubstancePath string
	tmpMatterPath    string
	stagedMatter     []byte
	expectedSHA256   string
	uploading        bool // guarded by HTTPSubstanceGetter.mu
	uploadBody       io.Closer
	uploadDeadline   time.Time
	baseBytes        int64
	baseLines        int64
	framing          string
	readOffset       int64
	readLength       int64
	readRanged       bool
	readDelivered    []byteInterval

	// authz is set for a lease opened for an external caller; nil otherwise. It is
	// written once before the handle is returned and read on every use.
	authz *leaseAuthz
}

type byteInterval struct{ start, end int64 }

// expired
//
// Functional role (Brique DSL):
// - evaluate lease expiry against current time.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
//
// - now time.Time.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Returns `true` only when the lease has a non-zero expiry instant strictly before `now`.
//

func (s *lease) expired(now time.Time) bool {
	return !s.expires.IsZero() && now.After(s.expires)
}

// -----------------------------
// HTTPSubstanceGetter
// -----------------------------

type HTTPSubstanceGetter struct {
	ml      *MatterLoop
	ownerMu sync.Mutex
	owners  map[*MatterLoop]struct{}

	mu     sync.RWMutex
	leases map[string]*lease // rid -> lease

	// Server
	srvMu      sync.Mutex
	srv        *http.Server
	ln         net.Listener
	baseURL    string
	publicURL  string
	listenAddr string

	defaultTTL           time.Duration
	maxUploadBytes       int64
	uploadIdleTimeout    time.Duration
	uploadMaxDuration    time.Duration
	transferSlots        chan struct{}
	leaseMaxLife         time.Duration
	readIdleTimeout      time.Duration
	byteBudget           *ownerByteBudget
	heavyBudget          *ownerByteBudget
	authorizationRecheck time.Duration
	now                  func() time.Time
	authorizeLease       func(*lease) bool

	// sweeper lifecycle
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
	sweep time.Duration
}

// NewHTTPSubstanceGetter
//
// Functional role (Brique DSL):
// - initialize HTTP substance getter state, lease registry, lifecycle channels, and default timing policy.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - allocates lease registry, lifecycle channels, and timing configuration in memory.
//
// Inputs:
//
// - ml *MatterLoop.
//
//
// Outputs:
//
// - returns *HTTPSubstanceGetter.
//
//
// Contract:
// - Returns a stopped getter with empty lease state and no network side effects.
// - The getter reuses the provided `MatterLoop` pointer directly and does not validate it here.
//

func NewHTTPSubstanceGetter(ml *MatterLoop) *HTTPSubstanceGetter {
	return &HTTPSubstanceGetter{
		ml:                   ml,
		owners:               make(map[*MatterLoop]struct{}),
		leases:               make(map[string]*lease),
		defaultTTL:           timeToLiveLease * time.Second,
		maxUploadBytes:       defaultMaxUploadBytes,
		uploadIdleTimeout:    defaultUploadIdleTimeout,
		uploadMaxDuration:    defaultUploadMaxDuration,
		transferSlots:        make(chan struct{}, defaultMaxTransfers),
		leaseMaxLife:         defaultLeaseMaxLife,
		readIdleTimeout:      defaultReadIdleTimeout,
		authorizationRecheck: defaultAuthorizationRecheck,
		listenAddr:           "127.0.0.1:0",
		sweep:                sweeperPeriod * time.Second,
		done:                 make(chan struct{}),
	}
}

// Start starts the HTTP server (best-effort). Safe to call multiple times.
// Start
//
// Functional role (Brique DSL):
// - >sequence:
//   - no-op when HTTP server is already running
//   - bind localhost listener on ephemeral port
//   - install `/substance/` and `/healthz` handlers
//   - start HTTP serve loop and lease sweeper loop
//   - publish runtime base URL
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none directly from this function.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - binds a localhost listener and starts HTTP server goroutines.
// - publishes the runtime base URL.
//
// Inputs:
//
// - none.
//
// Outputs:
//
// - returns error.
//
// Contract:
// - Idempotent while running; repeated calls return without rebinding a second server.
// - After a successful stop, the closed `done` channel is not recreated; restarting the sweeper is therefore not supported on the same instance.
func (g *HTTPSubstanceGetter) Start() error {
	g.srvMu.Lock()
	defer g.srvMu.Unlock()
	if g.srv != nil {
		return nil
	}

	listenAddr := strings.TrimSpace(g.listenAddr)
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	publicURL, err := validateLeasePublicURL(g.publicURL)
	if err != nil {
		return err
	}
	if !isLoopbackListenAddress(listenAddr) && publicURL == "" {
		return errors.New("non-loopback lease_listen requires public_url")
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(substancePathPrefix, g.handleSubstance)
	if publicURL == "" {
		mux.HandleFunc(healthzPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}

	srv := &http.Server{
		Handler:           leaseHTTPHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	g.ln = ln
	g.srv = srv
	g.baseURL = publicURL
	if g.baseURL == "" {
		g.baseURL = "http://" + ln.Addr().String()
	}

	// Background accept loop.
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		_ = srv.Serve(ln)
	}()

	// Sweeper (expires leases + trace).
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.sweeperLoop()
	}()

	return nil
}

// Acquire attaches one Matter family to the instance-wide data plane. The
// listener starts with the first owner and remains available until the last
// Context releases it.
func (g *HTTPSubstanceGetter) Acquire(owner *MatterLoop) error {
	if g == nil || owner == nil {
		return errors.New("matter data-plane owner is nil")
	}
	g.ownerMu.Lock()
	defer g.ownerMu.Unlock()
	if _, exists := g.owners[owner]; exists {
		return nil
	}
	if len(g.owners) == 0 {
		if err := g.Start(); err != nil {
			return err
		}
	}
	g.owners[owner] = struct{}{}
	return nil
}

// Release detaches a Context, invalidates only its bearer leases, and stops the
// shared listener only after the final Context has left.
func (g *HTTPSubstanceGetter) Release(owner *MatterLoop) error {
	if g == nil || owner == nil {
		return nil
	}
	g.ownerMu.Lock()
	if _, exists := g.owners[owner]; !exists {
		g.ownerMu.Unlock()
		return nil
	}
	delete(g.owners, owner)
	last := len(g.owners) == 0
	if last {
		g.closeMatterLoopLeases(owner)
		defer g.ownerMu.Unlock()
		return g.Stop()
	}
	g.ownerMu.Unlock()
	g.closeMatterLoopLeases(owner)
	return nil
}

func (g *HTTPSubstanceGetter) closeMatterLoopLeases(owner *MatterLoop) int {
	if g == nil || owner == nil {
		return 0
	}
	g.mu.RLock()
	rids := make([]string, 0)
	for rid, lz := range g.leases {
		if g.ownerOf(lz) == owner {
			rids = append(rids, rid)
		}
	}
	g.mu.RUnlock()
	for _, rid := range rids {
		g.closeLease(rid, "context_stopped")
	}
	return len(rids)
}

func validateLeasePublicURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("public_url must be an exact HTTPS base URL without credentials, query or fragment")
	}
	return parsed.String(), nil
}

func isLoopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Stop stops the HTTP server and clears leases. Safe to call multiple times.
// Stop
//
// Functional role (Brique DSL):
// - >sequence:
//   - stop sweeper lifecycle
//   - detach current HTTP server and listener
//   - complete and clear all active leases
//   - shutdown server with timeout and wait for background goroutines to exit
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
//
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `closeLease`.
//
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace may be emitted indirectly for lease close/expire events.
//
// - On error:
//   - none.
//
// State/Storage Effects:
// - closes getter lifecycle once.
// - clears all active leases and completes them.
// - shuts down the HTTP server and listener.
//
// Inputs:
//
// - none.
//
// Outputs:
//
// - returns error.
//
// Contract:
// - Safe to call multiple times; all remaining leases are completed before return.
// - Active HTTP transfers are stopped before lease-owned temporary files are removed.
func (g *HTTPSubstanceGetter) Stop() error {
	// Stop sweeper
	g.once.Do(func() { close(g.done) })

	g.srvMu.Lock()
	srv := g.srv
	ln := g.ln
	g.srv = nil
	g.ln = nil
	g.baseURL = ""
	g.srvMu.Unlock()

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			// Force active connections closed before deleting their lease files.
			_ = srv.Close()
		}
		cancel()
		if ln != nil {
			_ = ln.Close()
		}
	}

	// Wait for the server and sweeper before cleaning lease-owned files.
	g.wg.Wait()

	g.mu.Lock()
	for _, lz := range g.leases {
		if lz != nil && (lz.mode == leaseWrite || lz.mode == leaseAppend) {
			_ = os.Remove(lz.tmpSubstancePath)
			_ = os.Remove(lz.tmpMatterPath)
		}
	}
	g.leases = make(map[string]*lease)
	g.mu.Unlock()

	return nil
}

// BaseURL
//
// Functional role (Brique DSL):
// - read current HTTP getter base URL under server lock.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - acquires and releases the server lock.
//
// Inputs:
//
// - none.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Returns empty string when the HTTP getter is not running.
// - Uses `srvMu` for synchronization even though it is a read-only query.
//

func (g *HTTPSubstanceGetter) BaseURL() string {
	g.srvMu.Lock()
	defer g.srvMu.Unlock()
	return g.baseURL
}

func (g *HTTPSubstanceGetter) currentTime() time.Time {
	if g != nil && g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *HTTPSubstanceGetter) leaseTimes(ttl time.Duration) (time.Time, time.Time) {
	now := g.currentTime()
	maxLife := g.leaseMaxLife
	if maxLife <= 0 {
		maxLife = defaultLeaseMaxLife
	}
	maximum := now.Add(maxLife)
	expires := now.Add(ttl)
	if expires.After(maximum) {
		expires = maximum
	}
	return expires, maximum
}

func (g *HTTPSubstanceGetter) allowed(lz *lease) bool {
	if g != nil && g.authorizeLease != nil {
		return g.authorizeLease(lz)
	}
	return lz != nil && lz.authz.allowed(lz.matter)
}

func (g *HTTPSubstanceGetter) renewLease(lz *lease) bool {
	if g == nil || lz == nil {
		return false
	}
	now := g.currentTime()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.leases[lz.rid] != lz {
		return false
	}
	maximum := lz.maxExpires
	if maximum.IsZero() {
		maxLife := g.leaseMaxLife
		if maxLife <= 0 {
			maxLife = defaultLeaseMaxLife
		}
		maximum = now.Add(maxLife)
		lz.maxExpires = maximum
	}
	if !now.Before(maximum) {
		return false
	}
	ttl := g.defaultTTL
	if ttl <= 0 {
		ttl = timeToLiveLease * time.Second
	}
	next := now.Add(ttl)
	if next.After(maximum) {
		next = maximum
	}
	lz.expires = next
	return true
}

// OpenReadLease
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate matter id and running HTTP getter
//   - derive brique-mode substance file path for target matter
//   - mint read lease identifiers and expiry
//   - register lease in local registry
//   - emit lease-open trace
//   - return HTTP read handle
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - handle fields in the returned `SubstanceHTTPHandle`.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `traceLease`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace when forwarding the lease-open trace.
// - On error:
//   - none.
//
// State/Storage Effects:
// - registers one read lease in memory.
// - emits one lease-open trace.
//
// Inputs:
// - ctxDir string, matterID string, rev int64, ttl time.Duration, sizeHint int64.
//
//
// Outputs:
//
// - returns (SubstanceHTTPHandle, error).
//
//
// Contract:
// - Fails when getter is not started or `matterID` is empty; TTL defaults to getter TTL when non-positive.
// - Lease registration does not verify on-disk substance existence; serving-time checks enforce file availability.
//

func (g *HTTPSubstanceGetter) OpenReadLease(
	ctxDir string,
	matterID string,
	ext string,
	rev int64,
	ttl time.Duration,
	sizeHint int64,
) (SubstanceHTTPHandle, error) {
	handle, err := g.OpenReadRangeLeaseFor(g.ml, ctxDir, matterID, ext, rev, ttl, 0, sizeHint)
	if err == nil {
		g.mu.Lock()
		if lz := g.leases[handle.RID]; lz != nil {
			lz.readRanged = false
		}
		g.mu.Unlock()
	}
	return handle, err
}

func (g *HTTPSubstanceGetter) OpenReadRangeLease(
	ctxDir string,
	matterID string,
	ext string,
	rev int64,
	ttl time.Duration,
	offset int64,
	length int64,
) (SubstanceHTTPHandle, error) {
	return g.OpenReadRangeLeaseFor(g.ml, ctxDir, matterID, ext, rev, ttl, offset, length)
}

func (g *HTTPSubstanceGetter) OpenReadRangeLeaseFor(
	owner *MatterLoop,
	ctxDir string,
	matterID string,
	ext string,
	rev int64,
	ttl time.Duration,
	offset int64,
	length int64,
) (SubstanceHTTPHandle, error) {
	if matterID == "" {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenReadLease: empty matter_id")
	}
	if ttl <= 0 {
		ttl = g.defaultTTL
	}
	if g.BaseURL() == "" {
		return SubstanceHTTPHandle{}, fmt.Errorf("substance http not running")
	}

	matterRoot := filepath.Join(ctxDir, matterDirName)
	subPath := filepath.Join(matterRoot, matterID+"."+ext)

	rid, err := randTok(18)
	if err != nil {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenReadLease: create resource id: %w", err)
	}
	tok, err := randTok(24)
	if err != nil {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenReadLease: create bearer token: %w", err)
	}
	expires, maxExpires := g.leaseTimes(ttl)
	lz := &lease{
		owner:         owner,
		rid:           rid,
		tok:           tok,
		mode:          leaseRead,
		matter:        matterID,
		rev:           rev,
		expires:       expires,
		maxExpires:    maxExpires,
		ctxDir:        ctxDir,
		matterRoot:    matterRoot,
		substancePath: subPath,
		readOffset:    offset,
		readLength:    length,
		readRanged:    true,
	}

	g.mu.Lock()
	g.leases[rid] = lz
	g.mu.Unlock()

	g.traceLease(circulation.ValueTraceLeaseOpen, "", lz, map[string]any{
		"mode":       "read",
		"expires_at": lz.expires.UnixNano(),
		"rev":        rev,
		"matter_id":  matterID,
	})

	h := SubstanceHTTPHandle{
		Kind:      "http",
		BaseURL:   g.BaseURL(),
		Path:      substancePathPrefix + rid,
		RID:       rid,
		Tok:       tok,
		ExpiresAt: lz.expires.UnixNano(),
		Rev:       rev,
		SizeHint:  length,
		Method:    "GET",
	}
	return h, nil
}

// OpenWriteLease
//
// Functional role (Brique DSL):
// - >sequence:
//   - validate matter id and running HTTP getter
//   - derive final and temporary brique-mode substance paths
//   - mint write lease identifiers and expiry
//   - bind write lease to expected revision, source intention, and new brique metadata
//   - register lease and emit lease-open trace
//   - return HTTP write handle
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - handle fields in the returned `SubstanceHTTPHandle`.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `traceLease`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace when forwarding the lease-open trace.
// - On error:
//   - none.
//
// State/Storage Effects:
// - stages a lease-unique matter descriptor file.
// - registers one write lease in memory.
// - emits one lease-open trace.
//
// Inputs:
// - ctxDir string, matterID string, expectedRev int64, srcIntentionID string,
//   newBrique map[string]any, stagedMatter []byte, ttl time.Duration.
//
//
// Outputs:
// - returns (SubstanceHTTPHandle, error).
//
//
// Contract:
// - Lease-owned temporary paths include the random RID and cannot collide with another lease.
// - Opening and streaming a lease does not hold the per-matter commit lock.
//

func (g *HTTPSubstanceGetter) OpenWriteLease(
	ctxDir string,
	matterID string,
	ext string,
	expectedRev int64,
	srcIntentionID string,
	newBrique map[string]any,
	stagedMatter []byte,
	ttl time.Duration,
) (SubstanceHTTPHandle, error) {
	return g.OpenWriteLeaseFor(g.ml, ctxDir, matterID, ext, expectedRev, srcIntentionID, newBrique, stagedMatter, ttl)
}

func (g *HTTPSubstanceGetter) OpenWriteLeaseFor(
	owner *MatterLoop,
	ctxDir string,
	matterID string,
	ext string,
	expectedRev int64,
	srcIntentionID string,
	newBrique map[string]any,
	stagedMatter []byte,
	ttl time.Duration,
	expectedSHA256 ...string,
) (SubstanceHTTPHandle, error) {
	if matterID == "" {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenWriteLease: empty matter_id")
	}
	if ttl <= 0 {
		ttl = g.defaultTTL
	}
	if g.BaseURL() == "" {
		return SubstanceHTTPHandle{}, fmt.Errorf("substance http not running")
	}

	rid, err := randTok(18)
	if err != nil {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenWriteLease: create resource id: %w", err)
	}
	tok, err := randTok(24)
	if err != nil {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenWriteLease: create bearer token: %w", err)
	}
	matterRoot := filepath.Join(ctxDir, matterDirName)
	finalSub := filepath.Join(matterRoot, matterID+"."+ext)
	tmpSub := finalSub + "." + rid + tmpSuffix
	tmpMatter := filepath.Join(matterRoot, matterID+matterDescriptorSuffix+"."+rid+tmpSuffix)

	if len(stagedMatter) == 0 {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenWriteLease: empty staged matter descriptor")
	}
	expires, maxExpires := g.leaseTimes(ttl)
	lz := &lease{
		owner:            owner,
		rid:              rid,
		tok:              tok,
		mode:             leaseWrite,
		matter:           matterID,
		rev:              expectedRev,
		srcIntentionID:   srcIntentionID,
		newBrique:        newBrique,
		expires:          expires,
		maxExpires:       maxExpires,
		ctxDir:           ctxDir,
		matterRoot:       matterRoot,
		substancePath:    finalSub,
		tmpSubstancePath: tmpSub,
		tmpMatterPath:    tmpMatter,
		stagedMatter:     append([]byte(nil), stagedMatter...),
		expectedSHA256:   firstString(expectedSHA256),
	}

	g.mu.Lock()
	g.leases[rid] = lz
	g.mu.Unlock()

	g.traceLease(circulation.ValueTraceLeaseOpen, srcIntentionID, lz, map[string]any{
		"mode":        "write",
		"expires_at":  lz.expires.UnixNano(),
		"expectedRev": expectedRev,
		"matter_id":   matterID,
	})

	h := SubstanceHTTPHandle{
		Kind:      "http",
		BaseURL:   g.BaseURL(),
		Path:      substancePathPrefix + rid,
		RID:       rid,
		Tok:       tok,
		ExpiresAt: lz.expires.UnixNano(),
		Rev:       expectedRev,
		NewBrique: newBrique,
		Method:    "PUT",
	}
	return h, nil
}

func (g *HTTPSubstanceGetter) OpenAppendLease(
	ctxDir, matterID, ext string,
	expectedRev, baseBytes, baseLines int64,
	framing, srcIntentionID string,
	stagedMatter []byte,
	ttl time.Duration,
) (SubstanceHTTPHandle, error) {
	return g.OpenAppendLeaseFor(g.ml, ctxDir, matterID, ext, expectedRev, baseBytes, baseLines, framing, srcIntentionID, stagedMatter, ttl)
}

func (g *HTTPSubstanceGetter) OpenAppendLeaseFor(
	owner *MatterLoop,
	ctxDir, matterID, ext string,
	expectedRev, baseBytes, baseLines int64,
	framing, srcIntentionID string,
	stagedMatter []byte,
	ttl time.Duration,
	expectedSHA256 ...string,
) (SubstanceHTTPHandle, error) {
	if matterID == "" || baseBytes < 0 || baseLines < 0 {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenAppendLease: invalid binding")
	}
	if ttl <= 0 {
		ttl = g.defaultTTL
	}
	if g.BaseURL() == "" {
		return SubstanceHTTPHandle{}, fmt.Errorf("substance http not running")
	}
	rid, err := randTok(18)
	if err != nil {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenAppendLease: create resource id: %w", err)
	}
	tok, err := randTok(24)
	if err != nil {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenAppendLease: create bearer token: %w", err)
	}
	matterRoot := filepath.Join(ctxDir, matterDirName)
	finalSub := filepath.Join(matterRoot, matterID+"."+ext)
	tmpSub := finalSub + "." + rid + ".append" + tmpSuffix
	tmpMatter := filepath.Join(matterRoot, matterID+matterDescriptorSuffix+"."+rid+".append"+tmpSuffix)
	if len(stagedMatter) == 0 {
		return SubstanceHTTPHandle{}, fmt.Errorf("OpenAppendLease: empty staged matter descriptor")
	}
	expires, maxExpires := g.leaseTimes(ttl)
	lz := &lease{
		owner: owner, rid: rid, tok: tok, mode: leaseAppend, matter: matterID, rev: expectedRev,
		srcIntentionID: srcIntentionID, expires: expires, maxExpires: maxExpires, ctxDir: ctxDir,
		matterRoot: matterRoot, substancePath: finalSub, tmpSubstancePath: tmpSub,
		tmpMatterPath: tmpMatter, stagedMatter: append([]byte(nil), stagedMatter...),
		baseBytes: baseBytes, baseLines: baseLines, framing: framing,
		expectedSHA256: firstString(expectedSHA256),
	}
	g.mu.Lock()
	g.leases[rid] = lz
	g.mu.Unlock()
	g.traceLease(circulation.ValueTraceLeaseOpen, srcIntentionID, lz, map[string]any{
		"mode": "append", "expires_at": lz.expires.UnixNano(), "expectedRev": expectedRev,
		"expected_byte_size": baseBytes, "matter_id": matterID,
	})
	return SubstanceHTTPHandle{
		Kind: circulation.ValueDataKindHTTP, BaseURL: g.BaseURL(), Path: substancePathPrefix + rid,
		RID: rid, Tok: tok, ExpiresAt: lz.expires.UnixNano(), Rev: expectedRev, Method: http.MethodPut,
	}, nil
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func optionalExpectedSHA256(params map[string]any) (string, error) {
	raw, exists := params[circulation.KeyExpectedSHA256]
	if !exists {
		return "", nil
	}
	value, ok := raw.(string)
	value = strings.ToLower(strings.TrimSpace(value))
	if !ok || len(value) != sha256.Size*2 {
		return "", errors.New("expected_sha256 must be a 64-character hexadecimal SHA-256 digest")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("expected_sha256 must be a 64-character hexadecimal SHA-256 digest")
	}
	return value, nil
}

// closeLease
//
// Functional role (Brique DSL):
// - remove one lease from registry, emit closure or expiry trace, and complete the lease once.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `traceLease`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace when forwarding lease-close or lease-expired traces.
// - On error:
//   - none.
//
// State/Storage Effects:
// - removes one lease from the in-memory registry.
// - removes lease-owned temporary files, except recovery evidence retained after a partial commit.
//
// Inputs:
//
// - rid string, why string.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Empty `rid` is ignored; unknown leases produce no effect.
//

func (g *HTTPSubstanceGetter) closeLease(rid string, why string) {
	if rid == "" {
		return
	}
	g.mu.Lock()
	lz := g.leases[rid]
	delete(g.leases, rid)
	g.mu.Unlock()
	if lz != nil {
		if lz.mode == leaseWrite || lz.mode == leaseAppend {
			if why != "fail:recovery_pending" {
				_ = os.Remove(lz.tmpSubstancePath)
			}
			if why != "fail:recovery_pending" {
				_ = os.Remove(lz.tmpMatterPath)
			}
		}
		if why == "expired" {
			g.traceLease(circulation.ValueTraceLeaseExpired, lz.srcIntentionID, lz, map[string]any{
				"why":       why,
				"matter_id": lz.matter,
				"rev":       lz.rev,
			})
		} else {
			g.traceLease(circulation.ValueTraceLeaseClose, lz.srcIntentionID, lz, map[string]any{
				"why":       why,
				"matter_id": lz.matter,
				"rev":       lz.rev,
			})
		}
	}
}

// sweeperLoop
//
// Functional role (Brique DSL):
// - periodically scan lease registry and close expired leases until getter shutdown.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `closeLease`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace may be emitted for expired leases.
// - On error:
//   - none.
//
// State/Storage Effects:
// - periodically scans the in-memory lease registry and closes expired leases.
//
// Inputs:
//
// - none.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Sweep period falls back to 1 second when unset or non-positive.
//

func (g *HTTPSubstanceGetter) sweeperLoop() {
	tick := g.sweep
	if tick <= 0 {
		tick = 1 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-g.done:
			return
		case now := <-t.C:
			expired := make([]string, 0, 8)
			bodies := make([]io.Closer, 0, 2)

			g.mu.Lock()
			for rid, lz := range g.leases {
				if lz == nil {
					continue
				}
				if lz.uploading && !lz.uploadDeadline.IsZero() && now.After(lz.uploadDeadline) {
					if lz.uploadBody != nil {
						bodies = append(bodies, lz.uploadBody)
					}
					lz.uploadBody = nil
					lz.uploadDeadline = time.Time{}
					lz.uploading = false
					expired = append(expired, rid)
				} else if !lz.uploading && lz.expired(now) {
					expired = append(expired, rid)
				}
			}
			g.mu.Unlock()
			for _, body := range bodies {
				_ = body.Close()
			}

			if len(expired) == 0 {
				continue
			}
			for _, rid := range expired {
				g.closeLease(rid, "expired")
			}
		}
	}
}

// handleSubstance
//
// Functional role (Brique DSL):
// - >sequence:
//   - parse request RID from `/substance/<rid>`
//   - require query token and resolve lease
//   - reject expired or token-mismatched leases
//   - branch on lease mode:
//     - `read` -> require `GET`, revalidate revision, stream file, then close lease
//     - `write` -> require `PUT`, receive upload and finalize through matter control-plane
//   - emit HTTP problem payloads on refusal paths
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - HTTP payload body for successful reads or writes.
// - On error:
//   - `circulation.KeyOK`
//   - `circulation.KeyReason`
//   - `circulation.KeyStatus`
//   - `circulation.KeyDetail`
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `traceLease`, `closeLease`, `serveFile`, and `receiveAndCommit`.
// - On error:
//   - trace fields may be emitted indirectly by `closeLease` on expiration or failure.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace may be emitted indirectly for lease lifecycle tracing.
// - On error:
//   - kind, ts, and trace may be emitted indirectly for lease lifecycle tracing.
//
// State/Storage Effects:
// - reads lease state from memory.
// - may stream file bytes to the HTTP response.
// - may receive upload bytes and finalize a write lease.
// - may close the lease after request completion or failure.
//
// Inputs:
//
// - w http.ResponseWriter, r *http.Request.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Request authorization is lease-scoped only: RID, token, expiry, method, and revision binding must match.
//

func (g *HTTPSubstanceGetter) handleSubstance(w http.ResponseWriter, r *http.Request) {
	// Path: /substance/<rid>
	rid := strings.TrimPrefix(r.URL.Path, substancePathPrefix)
	if rid == "" || strings.Contains(rid, "/") {
		writeProblem(w, http.StatusBadRequest, "bad_rid", rid, "", map[string]any{})
		return
	}

	// During the local transition both forms are accepted. Once a public URL is
	// configured only the header is accepted, so the token cannot enter proxy
	// access logs, browser history or Referer.
	tok := bearerToken(r.Header.Get("Authorization"))
	if tok == "" && g.publicURL == "" {
		tok = r.URL.Query().Get("tok")
	}
	if tok == "" {
		if g.publicURL != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeProblem(w, http.StatusUnauthorized, "missing_tok", rid, "", map[string]any{})
		return
	}

	// Lookup lease
	lz := g.getLease(rid)
	if lz == nil {
		if g.publicURL != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeProblem(w, http.StatusNotFound, "unknown_rid", rid, "", map[string]any{})
		return
	}
	now := g.currentTime()
	if lz.expired(now) {
		g.closeLease(rid, "expired")
		if g.publicURL != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeProblem(w, http.StatusUnauthorized, "expired", rid, "", map[string]any{})
		return
	}
	if subtleStringNe(tok, lz.tok) {
		if g.publicURL != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeProblem(w, http.StatusUnauthorized, "invalid_tok", rid, "", map[string]any{})
		return
	}
	// The right of an external caller is checked again on every use (phase 4 bis).
	if !g.allowed(lz) {
		g.traceLease(circulation.ValueTraceLeaseFail, lz.srcIntentionID, lz, map[string]any{
			"status": http.StatusForbidden, "reason": "revoked", "matter_id": lz.matter,
		})
		g.closeLease(rid, "revoked")
		writeProblem(w, http.StatusForbidden, "forbidden", rid, "", map[string]any{})
		return
	}
	if !g.renewLease(lz) {
		g.closeLease(rid, "max_life")
		if g.publicURL != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeProblem(w, http.StatusUnauthorized, "expired", rid, "", map[string]any{})
		return
	}
	select {
	case g.transferSlots <- struct{}{}:
		defer func() { <-g.transferSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeProblem(w, http.StatusServiceUnavailable, "transfer_capacity", rid, "", map[string]any{})
		return
	}

	switch lz.mode {
	case leaseRead:
		if r.Method != http.MethodGet {
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", rid, "GET", map[string]any{})
			return
		}
		// TOCTOU defense (read): check rev right before serving.
		if !g.revStillValidLease(lz) {
			g.closeLease(rid, "stale")
			writeProblem(w, http.StatusConflict, "stale", rid, "", map[string]any{})
			return
		}
		if g.serveFile(w, r, lz) {
			g.closeLease(rid, "read_done")
		}
		return

	case leaseWrite, leaseAppend:
		if r.Method != http.MethodPut {
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", rid, "PUT", map[string]any{})
			return
		}
		if !g.revStillValidLease(lz) {
			g.closeLease(rid, "stale")
			writeProblem(w, http.StatusConflict, "stale", rid, "", map[string]any{})
			return
		}
		if lz.owner.diskLow() {
			g.closeLease(rid, "disk_space_low")
			writeProblem(w, http.StatusInsufficientStorage, "disk_space_low", rid, "", map[string]any{})
			return
		}
		if status := g.claimWriteLease(rid, lz); status != "" {
			if status == "expired" {
				g.closeLease(rid, status)
				writeProblem(w, http.StatusUnauthorized, status, rid, "", map[string]any{})
				return
			}
			writeProblem(w, http.StatusConflict, status, rid, "", map[string]any{})
			return
		}
		g.receiveAndCommit(w, r, lz)
		// close is done inside receiveAndCommit on success/failure
		return

	default:
		writeProblem(w, http.StatusInternalServerError, "bad_lease", rid, "", map[string]any{})
		return
	}
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

// claimWriteLease marks a write lease as actively uploading. The sweeper does
// not expire active uploads, and a lease accepts at most one PUT.
func (g *HTTPSubstanceGetter) claimWriteLease(rid string, expected *lease) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	lz := g.leases[rid]
	if lz == nil || lz != expected {
		return "unknown_rid"
	}
	if lz.expired(g.currentTime()) {
		return "expired"
	}
	if lz.uploading {
		return "upload_in_progress"
	}
	lz.uploading = true
	maxDuration := g.uploadMaxDuration
	if maxDuration <= 0 {
		maxDuration = defaultUploadMaxDuration
	}
	now := g.currentTime()
	deadline := now.Add(maxDuration)
	if !lz.maxExpires.IsZero() && deadline.After(lz.maxExpires) {
		deadline = lz.maxExpires
	}
	if !deadline.After(now) {
		lz.uploading = false
		return "expired"
	}
	lz.uploadDeadline = deadline
	return ""
}

func (g *HTTPSubstanceGetter) bindUploadBody(lz *lease, body io.Closer) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.leases[lz.rid]
	if current != lz || !lz.uploading || lz.uploadDeadline.IsZero() {
		return false
	}
	lz.uploadBody = body
	return true
}

func (g *HTTPSubstanceGetter) finishUploadBody(lz *lease) {
	g.mu.Lock()
	if g.leases[lz.rid] == lz {
		lz.uploadBody = nil
		lz.uploadDeadline = time.Time{}
	}
	g.mu.Unlock()
}

type uploadDeadlineReader struct {
	reader     io.Reader
	controller *http.ResponseController
	idle       time.Duration
	maximum    time.Time
}

func (reader *uploadDeadlineReader) Read(buffer []byte) (int, error) {
	deadline := time.Now().Add(reader.idle)
	if deadline.After(reader.maximum) {
		deadline = reader.maximum
	}
	if err := reader.controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

// getLease
//
// Functional role (Brique DSL):
// - read one lease pointer from registry under read lock.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads the in-memory lease registry under a read lock.
//
// Inputs:
//
// - rid string.
//
//
// Outputs:
//
// - returns *lease.
//
//
// Contract:
// - Returns `nil` when the RID is unknown.
//

func (g *HTTPSubstanceGetter) getLease(rid string) *lease {
	g.mu.RLock()
	lz := g.leases[rid]
	g.mu.RUnlock()
	return lz
}

// revStillValid
//
// Functional role (Brique DSL):
// - compare expected revision against current catalog brique revision for one matter.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads the matter catalog through `catalogGetMatter`.
//
// Inputs:
//
// - matterID string, expected int64.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Missing matter, missing brique block, or missing revision all fail closed as invalid revision binding.
//

func (g *HTTPSubstanceGetter) revStillValid(matterID string, expected int64) bool {
	return revStillValidFor(g.ml, matterID, expected)
}

func (g *HTTPSubstanceGetter) revStillValidLease(lz *lease) bool {
	// we read rev from catalog brique.rev (projected) to keep O(1).
	owner := g.ownerOf(lz)
	if owner == nil || lz == nil || lz.matter == "" {
		return false
	}
	return revStillValidFor(owner, lz.matter, lz.rev)
}

func revStillValidFor(owner *MatterLoop, matterID string, expected int64) bool {
	if owner == nil || matterID == "" {
		return false
	}
	e, ok := owner.catalogGetMatter(matterID)
	if !ok {
		return false
	}
	syn := e.Brique
	if syn == nil {
		return false
	}
	raw, ok := syn[circulation.KeyRevision]
	if !ok {
		// if rev not present, treat as mismatch (strict)
		return false
	}
	cur := shared.AnyToInt64(raw)
	return cur == expected
}

func (g *HTTPSubstanceGetter) ownerOf(lz *lease) *MatterLoop {
	if lz != nil && lz.owner != nil {
		return lz.owner
	}
	if g != nil {
		return g.ml
	}
	return nil
}

// serveFile
//
// Functional role (Brique DSL):
// - stream leased substance file bytes to HTTP response for a valid read lease.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - HTTP body bytes are streamed to `w` on success.
// - On error:
//   - HTTP problem payloads are written via `writeProblem`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - raw file bytes in the HTTP response body.
// - On error:
//   - `circulation.KeyOK`
//   - `circulation.KeyReason`
//   - `circulation.KeyStatus`
//   - `circulation.KeyDetail`
//
// Produced Trace:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - opens and streams the leased substance file to the HTTP response.
//
// Inputs:
//
// - w http.ResponseWriter, _ *http.Request, lz *lease.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Missing substance file yields HTTP problem response; successful responses expose `Content-Length` when stat is available.
//

func (g *HTTPSubstanceGetter) serveFile(w http.ResponseWriter, r *http.Request, lz *lease) bool {
	// Best-effort open + stream
	f, err := os.Open(lz.substancePath)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "data_missing", lz.rid, "", map[string]any{
			circulation.KeyFile: filepath.Base(lz.substancePath), circulation.KeyMatterID: lz.matter,
		})
		return false
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "stat_failed", lz.rid, "", map[string]any{})
		return false
	}
	etag := fmt.Sprintf("\"brique-rev-%d\"", lz.rev)
	w.Header().Set("ETag", etag)
	if value := strings.TrimSpace(r.Header.Get("If-Range")); value != "" && value != etag {
		writeProblem(w, http.StatusConflict, "stale", lz.rid, "", map[string]any{})
		return false
	}
	total := st.Size()
	if lz.readRanged {
		total = lz.readLength
	}
	recheck := g.authorizationRecheck
	if recheck <= 0 || recheck > defaultAuthorizationRecheck {
		recheck = defaultAuthorizationRecheck
	}
	capture := &leaseResponseCapture{
		ResponseWriter: w,
		controller:     http.NewResponseController(w),
		writeIdle:      g.readIdleTimeout,
		authorize:      func() bool { return g.allowed(lz) },
		now:            g.currentTime,
		nextCheck:      g.currentTime().Add(recheck),
		recheck:        recheck,
	}
	if capture.writeIdle <= 0 {
		capture.writeIdle = defaultReadIdleTimeout
	}
	defer capture.clearWriteDeadline()
	if lz.readRanged {
		reader := io.NewSectionReader(f, lz.readOffset, lz.readLength)
		http.ServeContent(capture, r, filepath.Base(lz.substancePath), time.Time{}, reader)
	} else {
		http.ServeContent(capture, r, filepath.Base(lz.substancePath), time.Time{}, f)
	}
	if capture.revoked {
		g.traceLease(circulation.ValueTraceLeaseFail, lz.srcIntentionID, lz, map[string]any{
			"status": http.StatusForbidden, "reason": "revoked_during_transfer", "matter_id": lz.matter,
		})
		g.closeLease(lz.rid, "revoked")
		return false
	}
	status := capture.status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK && capture.written == total {
		return true
	}
	if status != http.StatusPartialContent {
		return false
	}
	start, end, rangedTotal, ok := parseLeaseContentRange(w.Header().Get("Content-Range"))
	if !ok || rangedTotal != total || capture.written != end-start+1 {
		return false
	}
	return g.recordReadDelivery(lz, start, end+1, total)
}

type leaseResponseCapture struct {
	http.ResponseWriter
	controller *http.ResponseController
	writeIdle  time.Duration
	status     int
	written    int64
	authorize  func() bool
	now        func() time.Time
	nextCheck  time.Time
	recheck    time.Duration
	revoked    bool
}

func (w *leaseResponseCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *leaseResponseCapture) Write(data []byte) (int, error) {
	if w.authorize != nil && w.now != nil && !w.now().Before(w.nextCheck) {
		if !w.authorize() {
			w.revoked = true
			return 0, errors.New("lease authorization revoked during transfer")
		}
		w.nextCheck = w.now().Add(w.recheck)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.controller != nil && w.writeIdle > 0 {
		_ = w.controller.SetWriteDeadline(time.Now().Add(w.writeIdle))
	}
	n, err := w.ResponseWriter.Write(data)
	w.written += int64(n)
	return n, err
}

func (w *leaseResponseCapture) clearWriteDeadline() {
	if w != nil && w.controller != nil {
		_ = w.controller.SetWriteDeadline(time.Time{})
	}
}

func parseLeaseContentRange(value string) (int64, int64, int64, bool) {
	var start, end, total int64
	if _, err := fmt.Sscanf(value, "bytes %d-%d/%d", &start, &end, &total); err != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func (g *HTTPSubstanceGetter) recordReadDelivery(lz *lease, start, end, total int64) bool {
	if g == nil || lz == nil || start < 0 || end <= start || end > total {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.leases[lz.rid]
	if current != lz {
		return false
	}
	intervals := append(append([]byteInterval(nil), lz.readDelivered...), byteInterval{start: start, end: end})
	slices.SortFunc(intervals, func(a, b byteInterval) int { return cmp.Compare(a.start, b.start) })
	merged := intervals[:0]
	for _, interval := range intervals {
		if len(merged) == 0 || interval.start > merged[len(merged)-1].end {
			merged = append(merged, interval)
			continue
		}
		if interval.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = interval.end
		}
	}
	lz.readDelivered = merged
	return len(merged) == 1 && merged[0].start == 0 && merged[0].end == total
}

// receiveAndCommit
//
// Functional role (Brique DSL):
// - >sequence:
//   - ensure target matter directory exists
//   - stream bounded request body into lease temporary substance file
//   - emit upload trace
//   - revalidate expected revision
//   - delegate finalization to MatterLoop control-plane
//   - emit success response and completion trace, then close lease
//   - on any failure, remove temp file, emit failure response/trace, and close lease
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - HTTP JSON success payload via `writeJSON`.
// - On error:
//   - HTTP problem payload via `failWrite` -> `writeProblem`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - `circulation.KeyOK`
//   - `circulation.KeyRevision`
//   - `circulation.KeyWarning` when finalize reports one.
// - On error:
//   - `circulation.KeyOK`
//   - `circulation.KeyReason`
//   - `circulation.KeyStatus`
//   - `circulation.KeyDetail`
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `traceLease`.
// - On error:
//   - trace fields may be emitted indirectly by `failWrite` and `closeLease`.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace when forwarding upload/done lease traces.
// - On error:
//   - kind, ts, and trace when forwarding failure or close traces.
//
// State/Storage Effects:
// - creates the target matter directory when absent.
// - writes uploaded bytes to the lease temporary substance file.
// - delegates final authoritative commit to `finalizeWriteLeaseAfterUpload`.
// - closes the lease on success or failure.
//
// Inputs:
//
// - w http.ResponseWriter, r *http.Request, lz *lease.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Data-plane writes only the temporary payload file; authoritative commit is delegated to `finalizeWriteLeaseAfterUpload`.
//

func (g *HTTPSubstanceGetter) receiveAndCommit(w http.ResponseWriter, r *http.Request, lz *lease) {
	// Ensure matter root exists (flat layout: shared by all matter ids)
	if err := os.MkdirAll(lz.matterRoot, 0o755); err != nil {
		g.failWrite(w, lz, http.StatusInternalServerError, "mkdir", err)
		return
	}
	if len(lz.stagedMatter) != 0 {
		if err := os.WriteFile(lz.tmpMatterPath, lz.stagedMatter, 0o644); err != nil {
			g.failWrite(w, lz, http.StatusInternalServerError, "stage_descriptor", err)
			return
		}
		lz.stagedMatter = nil
	} else if _, err := os.Stat(lz.tmpMatterPath); err != nil {
		g.failWrite(w, lz, http.StatusInternalServerError, "missing_descriptor", err)
		return
	}

	// Write tmp substance file
	tmpf, err := os.Create(lz.tmpSubstancePath)
	if err != nil {
		g.failWrite(w, lz, http.StatusInternalServerError, "tmp create", err)
		return
	}

	// Stream body -> tmp file
	maxBytes := g.maxUploadBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxUploadBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if !g.bindUploadBody(lz, r.Body) {
		_ = r.Body.Close()
		_ = tmpf.Close()
		g.failWrite(w, lz, http.StatusConflict, "upload_expired", nil)
		return
	}
	idleTimeout := g.uploadIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultUploadIdleTimeout
	}
	reader := &uploadDeadlineReader{reader: r.Body, controller: http.NewResponseController(w),
		idle: idleTimeout, maximum: lz.uploadDeadline}
	digest := sha256.New()
	n, cpErr := io.Copy(io.MultiWriter(&diskGuardWriter{next: tmpf, owner: lz.owner}, digest), reader)
	_ = r.Body.Close()
	g.finishUploadBody(lz)
	_ = tmpf.Sync()
	_ = tmpf.Close()
	if cpErr != nil {
		_ = os.Remove(lz.tmpSubstancePath)
		if errors.Is(cpErr, errUploadDiskLow) {
			g.failWrite(w, lz, http.StatusInsufficientStorage, "disk_space_low", nil)
			return
		}
		// Detect MaxBytesReader overflow reliably.
		var mbe *http.MaxBytesError
		if errors.As(cpErr, &mbe) {
			g.failWrite(w, lz, http.StatusRequestEntityTooLarge, "too_large", cpErr)
			return
		}
		if errors.Is(cpErr, http.ErrBodyReadAfterClose) {
			g.failWrite(w, lz, http.StatusBadRequest, "copy_failed", cpErr)
			return
		}
		g.failWrite(w, lz, http.StatusBadRequest, "copy_failed", cpErr)
		return
	}
	actualSHA256 := hex.EncodeToString(digest.Sum(nil))
	if lz.expectedSHA256 != "" && subtleStringNe(actualSHA256, lz.expectedSHA256) {
		_ = os.Remove(lz.tmpSubstancePath)
		g.failWrite(w, lz, http.StatusUnprocessableEntity, "sha256_mismatch", nil)
		return
	}

	g.traceLease(circulation.ValueTraceLeaseUpload, lz.srcIntentionID, lz, map[string]any{
		"bytes":     n,
		"matter_id": lz.matter,
	})

	// IMPORTANT: data-plane ends here.
	// We only receive bytes into the lease tmp file.
	// Finalization (atomic replace + matter.json commit + catalog rev bump) is delegated to
	// a control-plane function associated with matter.write.
	owner := g.ownerOf(lz)
	if owner == nil {
		_ = os.Remove(lz.tmpSubstancePath)
		g.failWrite(w, lz, http.StatusInternalServerError, "missing matter loop", nil)
		return
	}

	// The right may have been withdrawn while the body was uploading: nothing is
	// committed for a caller that no longer holds it.
	if !g.allowed(lz) {
		_ = os.Remove(lz.tmpSubstancePath)
		g.failWrite(w, lz, http.StatusForbidden, "forbidden", nil)
		return
	}

	// TOCTOU defense (write): after upload, re-check rev *just before finalize*.
	// finalizeWriteLeaseAfterUpload also checks, but we do it here to fail fast / cleaner HTTP.
	if !g.revStillValidLease(lz) {
		_ = os.Remove(lz.tmpSubstancePath)
		g.failWrite(w, lz, http.StatusConflict, "stale", nil)
		return
	}

	if lz.mode == leaseAppend {
		result, err := owner.finalizeAppendLeaseAfterUpload(lz, n)
		if err != nil {
			if _, journalErr := os.Stat(matterTransactionPath(lz.matterRoot, lz.matter)); journalErr == nil {
				g.failWrite(w, lz, http.StatusInternalServerError, "recovery_pending", err)
				return
			}
			_ = os.Remove(lz.tmpSubstancePath)
			if errors.Is(err, errWriteLeaseStale) {
				g.failWrite(w, lz, http.StatusConflict, "stale", err)
				return
			}
			g.failWrite(w, lz, http.StatusInternalServerError, "finalize_failed", err)
			return
		}
		result[circulation.KeyMessageSHA256] = actualSHA256
		writeJSON(w, http.StatusOK, result)
		g.traceLease(circulation.ValueTraceLeaseDone, lz.srcIntentionID, lz, map[string]any{"bytes": n, "matter_id": lz.matter})
		g.closeLease(lz.rid, "append_done")
		return
	}

	newRev, warning, err := owner.finalizeWriteLeaseAfterUpload(lz, n)
	if err != nil {
		// A journal means commit began. Preserve all staged files so startup can
		// deterministically roll the transaction forward.
		if _, journalErr := os.Stat(matterTransactionPath(lz.matterRoot, lz.matter)); journalErr == nil {
			g.failWrite(w, lz, http.StatusInternalServerError, "recovery_pending", err)
			return
		}
		_ = os.Remove(lz.tmpSubstancePath)
		if errors.Is(err, errWriteLeaseStale) {
			g.failWrite(w, lz, http.StatusConflict, "stale", err)
			return
		}
		g.failWrite(w, lz, http.StatusInternalServerError, "finalize_failed", err)
		return
	}

	resp := map[string]any{circulation.KeyOK: true, circulation.KeyRevision: newRev, circulation.KeyMessageSHA256: actualSHA256}
	if warning != "" {
		resp[circulation.KeyWarning] = warning
	}
	writeJSON(w, http.StatusOK, resp)

	g.traceLease(circulation.ValueTraceLeaseDone, lz.srcIntentionID, lz, map[string]any{
		"matter_id": lz.matter,
		"new_rev":   newRev,
		"warning":   warning,
	})
	g.closeLease(lz.rid, "write_done")
}

// failWrite
//
// Functional role (Brique DSL):
// - emit write-failure trace, write HTTP problem payload, and close failed write lease.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - HTTP problem payload via `writeProblem`.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - `circulation.KeyOK`
//   - `circulation.KeyReason`
//   - `circulation.KeyStatus`
//   - `circulation.KeyDetail`
//
// Produced Trace:
// - Valid:
//   - trace fields may be emitted indirectly by `traceLease`.
// - On error:
//   - trace fields may be emitted indirectly by `traceLease`.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - kind, ts, and trace when forwarding the write-failure trace.
//
// State/Storage Effects:
// - emits a lease-failure trace.
// - writes an HTTP problem response.
// - closes the failed lease.
//
// Inputs:
//
// - w http.ResponseWriter, lz *lease, status int, reason string, err error.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Always closes the lease after emitting the failure response.
//

func (g *HTTPSubstanceGetter) failWrite(w http.ResponseWriter, lz *lease, status int, reason string, err error) {
	details := map[string]any{
		circulation.KeyMatterID: lz.matter,
		circulation.KeyRevision: lz.rev,
	}
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}

	g.traceLease(circulation.ValueTraceLeaseFail, lz.srcIntentionID, lz, map[string]any{
		"status":    status,
		"reason":    reason,
		"matter_id": lz.matter,
		"rev":       lz.rev,
		"err":       errorText,
	})

	writeProblem(w, status, reason, lz.rid, "", details)
	g.closeLease(lz.rid, "fail:"+reason)
}

func leaseHTTPHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header := writer.Header()
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(request.URL.Path, substancePathPrefix) {
			header.Set("Content-Disposition", "attachment")
		}
		next.ServeHTTP(writer, request)
	})
}

// randTok
//
// Functional role (Brique DSL):
// - generate random URL-safe token for lease RID or bearer token fields.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - reads random bytes from the system RNG.
//
// Inputs:
//
// - nBytes int.
//
//
// Outputs:
//
// - returns string.
//
//
// Contract:
// - Falls back to 18 random bytes when requested size is non-positive.
//

func randTok(nBytes int) (string, error) {
	if nBytes <= 0 {
		nBytes = 18
	}
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writeJSON
//
// Functional role (Brique DSL):
// - write one JSON HTTP response with content type and status code.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - HTTP JSON payload with status code.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - keys provided by `v`, serialized as JSON.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - writes HTTP headers and a JSON body to the response writer.
//
// Inputs:
//
// - w http.ResponseWriter, status int, v any.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - JSON encoding errors are ignored after headers are sent.
//

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeProblem
//
// Functional role (Brique DSL):
// - normalize HTTP error payload with reason, status, RID, expected method, and detail map.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - HTTP JSON problem payload with status code.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - `circulation.KeyOK`
//   - `circulation.KeyReason`
//   - `circulation.KeyStatus`
//   - `circulation.KeyDetail`
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - writes an HTTP JSON problem response.
//
// Inputs:
//
// - w http.ResponseWriter, status int, reason string, rid string, expect string, details map[string]any.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - Always writes a JSON payload with `ok=false`.
//

func writeProblem(w http.ResponseWriter, status int, reason string, rid string, expect string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	if rid != "" {
		details[circulation.KeyRID] = rid
	}
	if expect != "" {
		details[circulation.KeyExpectedMethod] = expect
	}
	payload := map[string]any{
		circulation.KeyOK:     false,
		circulation.KeyReason: reason,
		circulation.KeyStatus: status,
		circulation.KeyDetail: details,
	}
	writeJSON(w, status, payload)
}

// subtleStringNe
//
// Functional role (Brique DSL):
// - compare two strings in constant-time style and report non-equality.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// State/Storage Effects:
// - none.
//
// Inputs:
//
// - a, b string.
//
//
// Outputs:
//
// - returns bool.
//
//
// Contract:
// - Length mismatch returns `true` immediately; equal-length strings are compared bytewise without early exit.
//

func subtleStringNe(a, b string) bool {
	if len(a) != len(b) {
		return true
	}
	var out byte
	for i := 0; i < len(a); i++ {
		out |= a[i] ^ b[i]
	}
	return out != 0
}

// traceLease
//
// Functional role (Brique DSL):
// - enrich lease trace details and emit one Matter trace event through junction tracing.
//
//
// Expected Message Fields:
// - none.
//
// Expected Params Keys/values:
// - none.
//
// Produced Response Fields:
// - Valid:
//   - none directly from this function.
// - On error:
//   - none directly from this function.
//
// Produced Response Payload Keys/values:
// - Valid:
//   - none.
// - On error:
//   - none.
//
// Produced Trace:
// - Valid:
//   - trace.ts via `junction.TraceEmit`.
//   - trace.tracekind via `junction.TraceEmit`.
//   - trace.family via `junction.TraceEmit`.
//   - trace.intentionid via `junction.TraceEmit`.
//   - trace.msgkind via `junction.TraceEmit`.
//   - trace.usertext via `junction.TraceEmit`.
// - On error:
//   - none.
//
// Produced Outbound Message:
// - Valid:
//   - kind, ts, and trace when forwarding the lease trace through junction tracing.
// - On error:
//   - none.
//
// State/Storage Effects:
// - enriches the details map and emits one trace event through junction tracing.
//
// Inputs:
//
// - traceKind string, intentionID string, lz *lease, details map[string]any.
//
//
// Outputs:
//
// - no direct return value; effects are produced via state updates, emitted messages, or filesystem I/O.
//
//
// Contract:
// - No-op when MatterLoop frame is unavailable; missing lease fields are filled into `details` only when absent.
//

func (g *HTTPSubstanceGetter) traceLease(traceKind string, intentionID string, lz *lease, details map[string]any) {
	owner := g.ownerOf(lz)
	if owner == nil || owner.frame == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	if lz != nil {
		if _, ok := details[circulation.KeyRID]; !ok && lz.rid != "" {
			details[circulation.KeyRID] = lz.rid
		}
		if _, ok := details[circulation.KeyMatterID]; !ok && lz.matter != "" {
			details[circulation.KeyMatterID] = lz.matter
		}
		if _, ok := details[circulation.KeyRevision]; !ok && lz.rev != 0 {
			details[circulation.KeyRevision] = lz.rev
		}
		if _, ok := details[circulation.KeyMode]; !ok && lz.mode != "" {
			details[circulation.KeyMode] = string(lz.mode)
		}
	}

	b, _ := json.Marshal(details)
	tw := circulation.TraceWire{
		TraceKind:   traceKind,
		Family:      circulation.ValueOriginMatter,
		IntentionId: intentionID,
		MsgKind:     "substance_http",
		ReasonCode:  "",
		UserText:    string(b),
	}
	junction.TraceEmit(owner.frame, tw)
}
