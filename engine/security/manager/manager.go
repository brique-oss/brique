/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

// Package manager owns transactional startup and reload of the complete
// instance security runtime. It is host-local and never reads security state
// from the Brique world.
package manager

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"brique_engine/junction"
	"brique_engine/security/activation"
	"brique_engine/security/audit"
	"brique_engine/security/bootstrap"
	"brique_engine/security/pending"
	"brique_engine/security/replay"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const (
	defaultPolicyID      = "main"
	defaultPolicyFile    = "trust-policy.json"
	defaultBootstrapFile = "bootstrap.json"
	defaultReplayFile    = "replay.journal"
	maxGenerationEntries = 128
	maxRetainedRuntimes  = 32
)

var (
	ErrNoSuccessor = errors.New("no exact successor security generation")
	// ErrPublication means the new durable generation was committed but could
	// not be made visible to routing. The process must stop rather than keep
	// serving with a runtime older than its rollback-resistant activation state.
	ErrPublication = errors.New("durable security activation could not be published")
)

type Options struct {
	SecurityDirectory string
	// PolicyDirectory, when set, is the instance's security folder: the signed policy of
	// each generation is read from <PolicyDirectory>/generations/<g>/trust-policy.json, with
	// the ordinary permissions of a versioned file, and the machine's single bootstrap
	// (<SecurityDirectory>/bootstrap.json) applies to every generation. Empty keeps the
	// layout where everything lives under SecurityDirectory.
	PolicyDirectory         string
	InitialGeneration       string
	PolicyID                string
	AllowedOwnerUIDs        []uint32
	RequireReadOnlyKeyFiles bool
	SignerExecutor          signer.Options
	ReplayLimits            replay.Limits
	ReplayBatch             replay.BatchOptions
	ReplayRetention         time.Duration
	// ReplayMaintenance is how often the replay journal is compacted and its finished
	// entries forgotten: zero means the default (one minute), negative disables it.
	ReplayMaintenance time.Duration
	PendingLimits     pending.Limits
	Now               func() time.Time
	// AuditMaxFileMiB and AuditRetentionDays tune the denial journal under
	// <SecurityDirectory>/audit; zero selects the defaults (64 MiB, 14 days).
	AuditMaxFileMiB    int
	AuditRetentionDays int
}

type Manager struct {
	mu         sync.Mutex
	options    Options
	store      *activation.Store
	registry   *junction.ContextCommRegistry
	replay     *replay.Service
	audit      *audit.Logger
	pending    *pending.Registry
	active     *securityruntime.Runtime
	authority  trust.Authority
	generation string
	runtimes   []*securityruntime.Runtime
	closed     bool
}

func Start(options Options, catalog trust.Catalog, registry *junction.ContextCommRegistry) (*Manager, error) {
	options = normalizeOptions(options)
	if registry == nil || options.SecurityDirectory == "" || options.PolicyID == "" {
		return nil, fmt.Errorf("complete security manager configuration is required")
	}
	store, err := activation.NewStore(options.SecurityDirectory, "", options.AllowedOwnerUIDs)
	if err != nil {
		return nil, err
	}
	state, found, err := store.Load()
	if err != nil {
		return nil, err
	}
	generation := options.InitialGeneration
	chosen := false
	if generation == "" && !found {
		// A machine that never ran this instance starts from the newest generation it finds;
		// the signature and the trust anchor decide whether it is valid.
		generation = newestGeneration(options.policyBase())
		chosen = generation != ""
	}
	if found {
		generation = state.ActiveGeneration
		if options.InitialGeneration != "" && options.InitialGeneration != generation {
			return nil, fmt.Errorf("configured generation conflicts with durable activation")
		}
	}
	if generation == "" || !shared.ValidProtectedRelativePath(generation) || strings.Contains(generation, "/") {
		return nil, fmt.Errorf("initial security generation is required")
	}

	bootstrapCandidate, decoded, err := loadGeneration(options, generation)
	if err != nil {
		return nil, err
	}
	rollbackCandidate := true
	defer func() {
		if rollbackCandidate {
			_ = bootstrapCandidate.Close()
		}
	}()
	if bootstrapCandidate.InstanceID() != decoded.InstanceID() || decoded.PolicyID() != options.PolicyID {
		return nil, fmt.Errorf("bootstrap and trust policy binding mismatch")
	}
	trustRuntime, err := trust.NewRuntime(decoded.InstanceID(), decoded.PolicyID())
	if err != nil {
		return nil, err
	}
	now := options.Now().UTC()
	var initialCandidate *trust.Candidate
	if found {
		floor, _ := strconv.ParseInt(state.SecurityTimeFloorMS, 10, 64)
		if state.InstanceID != decoded.InstanceID() || state.PolicyID != decoded.PolicyID() || now.UnixMilli() < floor {
			return nil, fmt.Errorf("durable activation binding or security clock mismatch")
		}
		if err := trustRuntime.Recover(decoded, catalog, state.TrustRecord()); err != nil {
			return nil, err
		}
	} else if options.PolicyDirectory != "" || chosen {
		// The instance carries its whole history, or the generation was not named: a machine
		// that has never run it starts from the newest generation rather than replaying
		// every revision.
		initialCandidate, err = trustRuntime.PrepareAdoption(decoded, catalog)
		if err != nil {
			return nil, err
		}
	} else {
		initialCandidate, err = trustRuntime.Prepare(decoded, catalog)
		if err != nil {
			return nil, err
		}
	}

	// Prepare every fallible host-local dependency before first activation is
	// made durable. A failed replay recovery must not select a generation that
	// the process never became capable of serving.
	replayRuntime, err := openReplay(options)
	if err != nil {
		return nil, err
	}
	pendingResponses, err := pending.New(options.PendingLimits)
	if err != nil {
		_ = replayRuntime.Close()
		return nil, err
	}
	if initialCandidate != nil {
		if err := trustRuntime.Commit(initialCandidate, func(record trust.ActivationRecord) error {
			initial, err := activation.FromRecord(record, generation, now.UnixMilli())
			if err != nil {
				return err
			}
			return store.Persist(initial)
		}); err != nil {
			_ = replayRuntime.Close()
			return nil, err
		}
	}
	auditLog, err := audit.Open(audit.Options{
		Directory:     filepath.Join(options.SecurityDirectory, "audit"),
		MaxFileBytes:  int64(options.AuditMaxFileMiB) << 20,
		RetentionDays: options.AuditRetentionDays,
		Now:           options.Now,
	})
	if err != nil {
		_ = replayRuntime.Close()
		return nil, fmt.Errorf("denial journal unavailable: %w", err)
	}
	peers, err := peerDirectory(bootstrapCandidate, decoded)
	if err != nil {
		_ = auditLog.Close()
		_ = replayRuntime.Close()
		return nil, err
	}
	complete, err := securityruntime.New(
		decoded.InstanceID(), bootstrapCandidate.Signers(), trustRuntime,
		peers, replayRuntime, pendingResponses,
	)
	if err != nil {
		_ = auditLog.Close()
		_ = replayRuntime.Close()
		return nil, err
	}
	if err := registry.InstallSecurityRuntime(complete); err != nil {
		_ = auditLog.Close()
		_ = complete.Close()
		return nil, err
	}
	registry.SetAuditRecorder(auditLog)
	rollbackCandidate = false
	return &Manager{
		options: options, store: store, registry: registry, replay: replayRuntime, audit: auditLog, pending: pendingResponses,
		active: complete, authority: bootstrapCandidate.PolicyAuthority(), generation: generation,
		runtimes: []*securityruntime.Runtime{complete},
	}, nil
}

func (manager *Manager) Runtime() *securityruntime.Runtime {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.active
}

func (manager *Manager) Reload(catalog trust.Catalog) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || manager.active == nil {
		return fmt.Errorf("security manager cannot reload")
	}
	if len(manager.runtimes) >= maxRetainedRuntimes {
		// Each reload keeps the previous runtime alive for the admissions it still
		// serves; the bound is what keeps memory finite. Restarting clears it.
		return fmt.Errorf("security manager retains %d runtimes, the maximum: restart the engine before the next reload", maxRetainedRuntimes)
	}
	state, found, err := manager.store.Load()
	if err != nil || !found || state.ActiveGeneration != manager.generation ||
		state.ActivePolicyHash != manager.active.Trust().Active().PolicyHash() {
		return fmt.Errorf("durable activation changed outside the active manager")
	}
	now := manager.options.Now().UTC()
	floor, _ := strconv.ParseInt(state.SecurityTimeFloorMS, 10, 64)
	if now.UnixMilli() < floor {
		return fmt.Errorf("security clock moved behind durable floor")
	}

	generation, decoded, err := manager.findSuccessor(catalog)
	if err != nil {
		return err
	}
	bootstrapCandidate, verified, err := loadGeneration(manager.options, generation)
	if err != nil {
		return err
	}
	rollbackCandidate := true
	defer func() {
		if rollbackCandidate {
			_ = bootstrapCandidate.Close()
		}
	}()
	if verified.PolicyHash() != decoded.PolicyHash() || bootstrapCandidate.InstanceID() != decoded.InstanceID() ||
		bootstrapCandidate.PolicyAuthority().KeyID != manager.authority.KeyID ||
		!bytes.Equal(bootstrapCandidate.PolicyAuthority().PublicKey, manager.authority.PublicKey) {
		return fmt.Errorf("reload generation changed protected instance authority")
	}
	if _, err := manager.active.Trust().Prepare(decoded, catalog); err != nil {
		return err
	}
	record := trust.ActivationRecord{
		InstanceID: decoded.InstanceID(), PolicyID: decoded.PolicyID(), Revision: decoded.Revision(),
		PolicyHash: decoded.PolicyHash(), PreviousPolicyHash: manager.active.Trust().Active().PolicyHash(),
	}
	nextTrust, err := trust.NewRuntime(record.InstanceID, record.PolicyID)
	if err != nil {
		return err
	}
	if err := nextTrust.Recover(decoded, catalog, record); err != nil {
		return err
	}
	peers, err := peerDirectory(bootstrapCandidate, decoded)
	if err != nil {
		return err
	}
	next, err := securityruntime.New(
		record.InstanceID, bootstrapCandidate.Signers(), nextTrust, peers, manager.replay, manager.pending,
	)
	if err != nil {
		return err
	}
	nextState, err := activation.FromRecord(record, generation, maxInt64(floor, now.UnixMilli()))
	if err != nil {
		return err
	}
	if err := manager.store.Persist(nextState); err != nil {
		return err
	}
	if err := manager.registry.SwapSecurityRuntime(manager.active, next); err != nil {
		_ = next.Close()
		return fmt.Errorf("%w: %v", ErrPublication, err)
	}
	manager.active = next
	manager.generation = generation
	manager.runtimes = append(manager.runtimes, next)
	rollbackCandidate = false
	return nil
}

func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	var checkpointErr error
	state, found, err := manager.store.Load()
	if err != nil || !found || manager.active == nil ||
		state.ActiveGeneration != manager.generation ||
		state.ActivePolicyHash != manager.active.Trust().Active().PolicyHash() {
		checkpointErr = fmt.Errorf("security time checkpoint unavailable")
	} else {
		floor, _ := strconv.ParseInt(state.SecurityTimeFloorMS, 10, 64)
		record := trust.ActivationRecord{
			InstanceID: state.InstanceID, PolicyID: state.PolicyID,
			Revision: state.HighestRevision, PolicyHash: state.ActivePolicyHash,
			PreviousPolicyHash: state.PreviousPolicyHash,
		}
		checkpoint, checkpointBuildErr := activation.FromRecord(
			record, manager.generation, maxInt64(floor, manager.options.Now().UTC().UnixMilli()),
		)
		if checkpointBuildErr != nil {
			checkpointErr = checkpointBuildErr
		} else {
			checkpointErr = manager.store.Persist(checkpoint)
		}
	}
	runtimes := append([]*securityruntime.Runtime(nil), manager.runtimes...)
	manager.mu.Unlock()
	first := checkpointErr
	// Stop recording first so nothing is queued behind the final flush.
	manager.registry.SetAuditRecorder(nil)
	if err := manager.audit.Close(); err != nil && first == nil {
		first = err
	}
	for index := len(runtimes) - 1; index >= 0; index-- {
		if err := runtimes[index].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (manager *Manager) findSuccessor(catalog trust.Catalog) (string, *trust.DecodeResult, error) {
	entries, err := os.ReadDir(filepath.Join(manager.options.policyBase(), "generations"))
	if err != nil || len(entries) > maxGenerationEntries {
		return "", nil, fmt.Errorf("security generations are unavailable or unbounded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	type match struct {
		generation string
		decoded    *trust.DecodeResult
	}
	matches := make([]match, 0, 1)
	for _, entry := range entries {
		name := entry.Name()
		if name == manager.generation || !entry.IsDir() || !shared.ValidProtectedRelativePath(name) || strings.Contains(name, "/") {
			continue
		}
		content, err := readPolicyEnvelope(manager.options, name)
		if err != nil {
			continue
		}
		decoded, verifyErr := trust.VerifyAndDecode(content, manager.authority)
		shared.ZeroBytes(content)
		if verifyErr != nil {
			continue
		}
		if _, prepareErr := manager.active.Trust().Prepare(decoded, catalog); prepareErr == nil {
			matches = append(matches, match{generation: name, decoded: decoded})
		}
	}
	if len(matches) == 0 {
		return "", nil, ErrNoSuccessor
	}
	if len(matches) != 1 {
		return "", nil, fmt.Errorf("multiple exact successor security generations")
	}
	return matches[0].generation, matches[0].decoded, nil
}

func loadGeneration(options Options, generation string) (*bootstrap.Candidate, *trust.DecodeResult, error) {
	candidate, err := bootstrap.Load(bootstrap.Options{
		SecurityDirectory: options.SecurityDirectory,
		BootstrapFile:     bootstrapFileFor(options, generation),
		Generation:        generation, AllowedOwnerUIDs: options.AllowedOwnerUIDs,
		RequireReadOnlyKeyFiles: options.RequireReadOnlyKeyFiles, SignerExecutor: options.SignerExecutor,
	})
	if err != nil {
		return nil, nil, err
	}
	content, err := readPolicyEnvelope(options, generation)
	if err != nil {
		_ = candidate.Close()
		return nil, nil, err
	}
	defer shared.ZeroBytes(content)
	decoded, err := trust.VerifyAndDecode(content, candidate.PolicyAuthority())
	if err != nil {
		_ = candidate.Close()
		return nil, nil, err
	}
	return candidate, decoded, nil
}

// peerDirectory is the inventory of peers a generation carries: the ones the signed policy
// declares, plus those of a bootstrap that still lists its own. A principal named twice is
// refused rather than resolved.
func peerDirectory(candidate *bootstrap.Candidate, decoded *trust.DecodeResult) (*securityruntime.PeerDirectory, error) {
	peers := candidate.Peers().Peers()
	for _, peer := range decoded.ExternalPeers() {
		peers = append(peers, securityruntime.Peer{PrincipalID: peer.PrincipalID, AudienceInstanceID: peer.AudienceInstanceID,
			Interface: peer.Interface, Target: peer.Target, Endpoints: peer.Endpoints})
	}
	return securityruntime.NewPeerDirectory(peers)
}

func openReplay(options Options) (*replay.Service, error) {
	if err := shared.ValidateProtectedDirectory(options.SecurityDirectory, options.AllowedOwnerUIDs); err != nil {
		return nil, err
	}
	file, err := shared.OpenProtectedFile(filepath.Join(options.SecurityDirectory, defaultReplayFile), true, options.AllowedOwnerUIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("open protected replay journal")
	}
	ledger, err := replay.Open(file, options.ReplayLimits)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	service, err := replay.NewService(ledger, options.ReplayBatch, options.ReplayRetention)
	if err != nil {
		_ = ledger.Close()
		return nil, err
	}
	if interval := options.ReplayMaintenance; interval >= 0 {
		if interval == 0 {
			interval = replay.DefaultMaintenanceInterval
		}
		service.StartMaintenance(filepath.Join(options.SecurityDirectory, defaultReplayFile), interval, options.Now, func(err error) {
			fmt.Fprintf(os.Stderr, "Warning: replay journal maintenance failed: %v\n", err)
		})
	}
	return service, nil
}

func normalizeOptions(options Options) Options {
	if options.PolicyID == "" {
		options.PolicyID = defaultPolicyID
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReplayLimits.MaxEntries == 0 {
		options.ReplayLimits = replay.DefaultLimits()
	}
	if options.ReplayBatch.MaxBatch == 0 {
		options.ReplayBatch = replay.DefaultBatchOptions()
	}
	if options.ReplayRetention == 0 {
		options.ReplayRetention = time.Hour
	}
	if options.PendingLimits.MaxEntries == 0 {
		options.PendingLimits = pending.DefaultLimits()
	}
	return options
}

// policyBase is the directory that holds the generations.
func (options Options) policyBase() string {
	if options.PolicyDirectory != "" {
		return options.PolicyDirectory
	}
	return options.SecurityDirectory
}

// bootstrapFileFor is the bootstrap that applies to a generation: the machine's single one
// when the policy lives in the instance, the generation's own otherwise.
func bootstrapFileFor(options Options, generation string) string {
	if options.PolicyDirectory != "" {
		return defaultBootstrapFile
	}
	return generationFile(generation, defaultBootstrapFile)
}

// readPolicyEnvelope reads the signed policy of one generation. The content is not trusted
// by where it came from: the caller verifies its signature against the trust anchor.
func readPolicyEnvelope(options Options, generation string) ([]byte, error) {
	if options.PolicyDirectory != "" {
		return shared.ReadInstanceFile(options.PolicyDirectory, generationFile(generation, defaultPolicyFile), trust.MaxEnvelopeBytes)
	}
	return shared.ReadProtectedFile(options.SecurityDirectory, generationFile(generation, defaultPolicyFile), shared.ProtectedFileOptions{
		MaxBytes: trust.MaxEnvelopeBytes, AllowedOwnerUIDs: options.AllowedOwnerUIDs,
	})
}

var generationNamePattern = regexp.MustCompile(`^g[0-9]{6}$`)

// newestGeneration is the highest generation name in a policy directory, or "".
func newestGeneration(policyDirectory string) string {
	entries, err := os.ReadDir(filepath.Join(policyDirectory, "generations"))
	if err != nil || len(entries) > maxGenerationEntries {
		return ""
	}
	newest := ""
	for _, entry := range entries {
		if entry.IsDir() && generationNamePattern.MatchString(entry.Name()) && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	return newest
}

func generationFile(generation, name string) string {
	return filepath.ToSlash(filepath.Join("generations", generation, name))
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
