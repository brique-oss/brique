/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package manager

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/junction"
	"brique_engine/security/activation"
	"brique_engine/security/audit"
	"brique_engine/security/bootstrap"
	"brique_engine/security/replay"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
)

func TestStartReloadAndRecoverDurableGeneration(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	authority := trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}

	first := writeGeneration(t, directory, "g1", authority, authorityPrivate, "1", "", "https://first.example/brique")
	registry := junction.NewContextCommRegistry()
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	options := Options{
		SecurityDirectory: directory, InitialGeneration: "g1", RequireReadOnlyKeyFiles: true,
		Now: func() time.Time { return clock },
	}
	manager, err := Start(options, emptyCatalog(), registry)
	if err != nil {
		t.Fatal(err)
	}
	initial := manager.Runtime()
	if initial == nil || registry.SecurityRuntime() != initial || initial.Trust().Active().PolicyHash() != first.PolicyHash() {
		t.Fatal("initial complete runtime was not published")
	}
	peer, found := initial.Peer("partner-a")
	if !found || peer.Target != "https://first.example/brique" {
		t.Fatalf("initial peer = %+v, found=%v", peer, found)
	}

	second := writeGeneration(t, directory, "g2", authority, authorityPrivate, "2", first.PolicyHash(), "https://second.example/brique")
	clock = clock.Add(time.Minute)
	if err := manager.Reload(emptyCatalog()); err != nil {
		t.Fatal(err)
	}
	reloaded := manager.Runtime()
	if reloaded == initial || registry.SecurityRuntime() != reloaded || reloaded.Trust().Active().PolicyHash() != second.PolicyHash() {
		t.Fatal("successor complete runtime was not atomically published")
	}
	peer, found = reloaded.Peer("partner-a")
	if !found || peer.Target != "https://second.example/brique" {
		t.Fatalf("reloaded peer = %+v, found=%v", peer, found)
	}
	store, err := activation.NewStore(directory, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Load()
	if err != nil || !found || state.ActiveGeneration != "g2" || state.HighestRevision != "2" {
		t.Fatalf("durable state = %+v, found=%v, err=%v", state, found, err)
	}
	clock = clock.Add(2 * time.Minute)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	state, found, err = store.Load()
	if err != nil || !found || state.SecurityTimeFloorMS != "1793448180000" {
		t.Fatalf("shutdown time checkpoint = %+v, found=%v, err=%v", state, found, err)
	}

	recoveredRegistry := junction.NewContextCommRegistry()
	options.InitialGeneration = ""
	recovered, err := Start(options, emptyCatalog(), recoveredRegistry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	if recovered.Runtime().Trust().Active().Revision() != "2" || recoveredRegistry.SecurityRuntime() != recovered.Runtime() {
		t.Fatal("restart did not recover the exact durable generation")
	}
}

func TestReloadRequiresOneExactSuccessor(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	authority := trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}
	first := writeGeneration(t, directory, "g1", authority, authorityPrivate, "1", "", "https://first.example")
	manager, err := Start(Options{SecurityDirectory: directory, InitialGeneration: "g1", RequireReadOnlyKeyFiles: true}, emptyCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Reload(emptyCatalog()); !errors.Is(err, ErrNoSuccessor) {
		t.Fatalf("reload without successor = %v", err)
	}
	writeGeneration(t, directory, "g2", authority, authorityPrivate, "2", first.PolicyHash(), "https://second.example")
	writeGeneration(t, directory, "g2-alt", authority, authorityPrivate, "2", first.PolicyHash(), "https://alternative.example")
	if err := manager.Reload(emptyCatalog()); err == nil || errors.Is(err, ErrNoSuccessor) {
		t.Fatalf("ambiguous successor reload = %v", err)
	}
}

func TestFirstActivationDoesNotCommitBeforeReplayRecovery(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	writeGeneration(t, directory, "g1", trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}, authorityPrivate, "1", "", "https://first.example")
	linked := filepath.Join(directory, "linked-replay")
	writeProtected(t, linked, []byte("unsafe"), 0o600)
	if err := os.Link(linked, filepath.Join(directory, defaultReplayFile)); err != nil {
		t.Fatal(err)
	}
	_, err = Start(Options{SecurityDirectory: directory, InitialGeneration: "g1", RequireReadOnlyKeyFiles: true}, emptyCatalog(), junction.NewContextCommRegistry())
	if err == nil {
		t.Fatal("unsafe replay journal was accepted")
	}
	store, err := activation.NewStore(directory, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, loadErr := store.Load(); loadErr != nil || found {
		t.Fatalf("first activation persisted before replay recovery: found=%v err=%v", found, loadErr)
	}
}

func writeGeneration(t *testing.T, directory, generation string, authority trust.Authority, authorityPrivate ed25519.PrivateKey, revision, previous, target string) *trust.DecodeResult {
	t.Helper()
	generationDirectory := filepath.Join(directory, "generations", generation)
	if err := os.MkdirAll(generationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, "generations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(generationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	messagePublic, messagePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := trust.SigningKeyID(messagePublic)
	keyBytes, err := x509.MarshalPKCS8PrivateKey(messagePrivate)
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, filepath.Join(generationDirectory, "message.pk8"), keyBytes, 0o400)
	document := bootstrap.Document{
		Format: bootstrap.Format, FormatVersion: bootstrap.FormatVersion, InstanceID: "prod-a",
		ActiveMessageSigner: "message", PolicySignatureThreshold: "1",
		MessageSigners: []bootstrap.MessageSigner{{
			Name: "message", Purpose: "message-signing", Provider: "file", ExpectedKeyID: messageID,
			Config: bootstrap.MessageSignerConfig{KeyFile: filepath.ToSlash(filepath.Join("generations", generation, "message.pk8"))},
		}},
		ExternalPeers: []securityruntime.Peer{{
			PrincipalID: "partner-a", AudienceInstanceID: "prod-b", Interface: "secure-peer", Target: target,
		}},
		PolicyAuthorities: []bootstrap.PolicyAuthority{{
			KeyID: authority.KeyID, Algorithm: trust.SigningAlgorithm,
			PublicKey: base64.RawURLEncoding.EncodeToString(authority.PublicKey),
		}},
	}
	encodedDocument, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, filepath.Join(generationDirectory, defaultBootstrapFile), encodedDocument, 0o600)

	policy := trust.Policy{
		Format: trust.PolicyFormat, FormatVersion: trust.PolicyFormatVersion,
		PolicyID: defaultPolicyID, InstanceID: "prod-a", Revision: revision,
		PreviousPolicyHash: previous, IssuedAt: "2026-10-31T12:00:00Z",
		Principals: []trust.Principal{}, Scopes: []trust.Scope{},
	}
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := trust.CanonicalPolicy(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := trust.SignPolicy(canonical, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	envelopeBytes, err := json.Marshal(trust.Envelope{
		Policy: canonical,
		Proofs: []trust.Proof{{AuthorityKeyID: authority.KeyID, Algorithm: trust.SigningAlgorithm, Signature: signature}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, filepath.Join(generationDirectory, defaultPolicyFile), envelopeBytes, 0o600)
	decoded, err := trust.VerifyAndDecode(envelopeBytes, authority)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func writeProtected(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func emptyCatalog() trust.Catalog {
	return trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: map[string][]string{}}
}

func TestManagerPublishesTheDenialJournalAndFlushesItOnClose(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	authority := trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}
	writeGeneration(t, directory, "g1", authority, authorityPrivate, "1", "", "https://first.example/brique")
	registry := junction.NewContextCommRegistry()
	manager, err := Start(Options{SecurityDirectory: directory, InitialGeneration: "g1", RequireReadOnlyKeyFiles: true}, emptyCatalog(), registry)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, "audit"))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("audit directory = %v, %v", info, err)
	}
	registry.AuditRecorder().Record(audit.Record{Principal: "partner-a", Reason: audit.ReasonCapabilityDenied, Layer: audit.LayerCapability})
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if registry.AuditRecorder() != audit.Nop {
		t.Fatal("the journal stayed installed after Close")
	}
	records, err := audit.Read(filepath.Join(directory, "audit"), audit.Filter{})
	if err != nil || len(records) != 1 || records[0].Principal != "partner-a" {
		t.Fatalf("journal = %+v, %v", records, err)
	}
}

// A clock set back must not reopen what a later time had closed: the durable floor
// refuses a restart behind it, and refuses a reload while the clock is behind it.
func TestClockBehindTheDurableFloorIsRefused(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	authority := trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}
	first := writeGeneration(t, directory, "g1", authority, authorityPrivate, "1", "", "https://first.example/brique")

	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	options := Options{
		SecurityDirectory: directory, InitialGeneration: "g1", RequireReadOnlyKeyFiles: true,
		Now: func() time.Time { return clock },
	}
	started, err := Start(options, emptyCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatal(err)
	}

	// A reload while the clock is an hour behind the floor is refused and changes nothing.
	writeGeneration(t, directory, "g2", authority, authorityPrivate, "2", first.PolicyHash(), "https://second.example/brique")
	active := started.Runtime()
	clock = clock.Add(-time.Hour)
	if err := started.Reload(emptyCatalog()); err == nil {
		t.Fatal("a reload went through with the clock behind the durable floor")
	}
	if started.Runtime() != active {
		t.Fatal("a refused reload still replaced the runtime")
	}
	clock = clock.Add(time.Hour + time.Minute)
	if err := started.Reload(emptyCatalog()); err != nil {
		t.Fatalf("the reload was refused once the clock was right again: %v", err)
	}
	if err := started.Close(); err != nil {
		t.Fatal(err)
	}

	// A restart with the clock behind the checkpoint written at close is refused.
	options.InitialGeneration = ""
	clock = clock.Add(-2 * time.Hour)
	if restarted, err := Start(options, emptyCatalog(), junction.NewContextCommRegistry()); err == nil {
		_ = restarted.Close()
		t.Fatal("a restart went through with the clock behind the durable floor")
	}
	clock = clock.Add(2 * time.Hour)
	restarted, err := Start(options, emptyCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatalf("the restart was refused with a correct clock: %v", err)
	}
	_ = restarted.Close()
}

// The replay journal is maintained by the engine itself: finished entries past their
// retention are forgotten, so an instance that has served its entry limit's worth of
// requests keeps serving instead of refusing every signed request until a restart.
func TestTheEngineMaintainsItsReplayJournal(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	authority := trust.Authority{KeyID: authorityID, PublicKey: authorityPublic}
	writeGeneration(t, directory, "g1", authority, authorityPrivate, "1", "", "https://first.example/brique")

	var clockMu sync.Mutex
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	limits := replay.DefaultLimits()
	limits.MaxEntries = 30
	started, err := Start(Options{
		SecurityDirectory: directory, InitialGeneration: "g1", RequireReadOnlyKeyFiles: true,
		Now: now, ReplayLimits: limits, ReplayMaintenance: 10 * time.Millisecond,
	}, emptyCatalog(), junction.NewContextCommRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer started.Close()
	coordinator := started.Runtime().Replay()

	serve := func(index int) error {
		key := replay.Key{AudienceInstanceID: "prod-b", PrincipalID: "partner-a", IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", index)}
		hash := "bmr1_" + strings.Repeat("A", 43)
		reservation, err := coordinator.Reserve(context.Background(), key, hash, now().Add(time.Minute))
		if err != nil {
			return err
		}
		if reservation.Kind != replay.ReservationNew {
			return fmt.Errorf("reservation %d is %v", index, reservation.Kind)
		}
		if err := coordinator.MarkDispatched(key, hash); err != nil {
			return err
		}
		return coordinator.Complete(key, hash, []byte("answer"))
	}
	for index := 1; index <= limits.MaxEntries; index++ {
		if err := serve(index); err != nil {
			t.Fatalf("serving %d: %v", index, err)
		}
	}
	if err := serve(1000); !errors.Is(err, replay.ErrSaturated) {
		t.Fatalf("a full journal accepted new work: %v", err)
	}
	// Time passes beyond the retention of everything served so far.
	clockMu.Lock()
	clock = clock.Add(3 * time.Hour)
	clockMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := serve(2000); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("the engine never freed its replay journal: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
