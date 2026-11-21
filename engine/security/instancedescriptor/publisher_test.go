/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package instancedescriptor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"brique_engine/engine/security/signer"
)

type publisherSigner struct {
	base  signer.Signer
	mu    sync.Mutex
	count int
	fail  bool
}

func (value *publisherSigner) Descriptor() signer.Descriptor { return value.base.Descriptor() }
func (value *publisherSigner) Sign(ctx context.Context, purpose signer.SigningPurpose, message []byte) ([]byte, error) {
	value.mu.Lock()
	value.count++
	fail := value.fail
	value.mu.Unlock()
	if fail {
		return nil, errors.New("unavailable")
	}
	return value.base.Sign(ctx, purpose, message)
}
func (value *publisherSigner) Health(ctx context.Context) error { return value.base.Health(ctx) }
func (value *publisherSigner) Close() error                     { return value.base.Close() }
func (value *publisherSigner) calls() int {
	value.mu.Lock()
	defer value.mu.Unlock()
	return value.count
}
func (value *publisherSigner) refuse(refuse bool) {
	value.mu.Lock()
	defer value.mu.Unlock()
	value.fail = refuse
}

func publisherTemplate() PublicationTemplate {
	return PublicationTemplate{InstanceID: "prod-b", AudienceInstanceID: "prod-b",
		UIOrigins: []string{"https://ui.example"},
		Endpoints: []Endpoint{{Name: "main", URL: "wss://engine.example/brique/ui"}},
		Protocols: []string{"brique.browser-wss/1"}}
}

func TestPublisherRefreshesOnlyForTimeConfigurationOrKey(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	first := &publisherSigner{base: testSigner(t)}
	active := signer.Signer(first)
	publisher, err := NewPublisher(PublisherOptions{Signer: func() signer.Signer { return active },
		Lifetime: time.Minute, RefreshBefore: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	one, err := publisher.EnsureFresh(ctx, publisherTemplate(), now)
	if err != nil || first.calls() != 1 {
		t.Fatalf("initial publication = %v calls=%d", err, first.calls())
	}
	same, err := publisher.EnsureFresh(ctx, publisherTemplate(), now.Add(10*time.Second))
	if err != nil || same.Hash() != one.Hash() || first.calls() != 1 {
		t.Fatalf("unchanged publication hash=%q calls=%d err=%v", same.Hash(), first.calls(), err)
	}
	refreshed, err := publisher.EnsureFresh(ctx, publisherTemplate(), now.Add(46*time.Second))
	if err != nil || refreshed.Hash() == one.Hash() || first.calls() != 2 {
		t.Fatalf("timed refresh hash=%q calls=%d err=%v", refreshed.Hash(), first.calls(), err)
	}
	changedTemplate := publisherTemplate()
	changedTemplate.UIOrigins = []string{"https://new.example"}
	changed, err := publisher.EnsureFresh(ctx, changedTemplate, now.Add(47*time.Second))
	if err != nil || changed.Hash() == refreshed.Hash() || !changed.AllowsOrigin("https://new.example") || first.calls() != 3 {
		t.Fatalf("configuration refresh calls=%d err=%v", first.calls(), err)
	}
	second := &publisherSigner{base: testSigner(t)}
	active = second
	rotated, err := publisher.EnsureFresh(ctx, changedTemplate, now.Add(48*time.Second))
	if err != nil || rotated.Protected().KeyID == changed.Protected().KeyID || second.calls() != 1 {
		t.Fatalf("key rotation = %+v calls=%d err=%v", rotated.Protected(), second.calls(), err)
	}
}

func TestPublisherKeepsLastValueWhenReplacementFails(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	current := &publisherSigner{base: testSigner(t)}
	publisher, err := NewPublisher(PublisherOptions{Signer: func() signer.Signer { return current }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	valid, err := publisher.Refresh(ctx, publisherTemplate(), now)
	if err != nil {
		t.Fatal(err)
	}
	current.refuse(true)
	if _, err := publisher.Refresh(ctx, publisherTemplate(), now.Add(time.Second)); err == nil {
		t.Fatal("failed signer published a replacement")
	}
	if publisher.Current() == nil || publisher.Current().Hash() != valid.Hash() {
		t.Fatal("failed replacement cleared the last valid descriptor")
	}
	publisher.Clear()
	if publisher.Current() != nil {
		t.Fatal("Clear retained the descriptor")
	}
}

func TestPublisherCurrentIsRaceSafeDuringRefresh(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	current := &publisherSigner{base: testSigner(t)}
	publisher, err := NewPublisher(PublisherOptions{Signer: func() signer.Signer { return current }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := publisher.Refresh(ctx, publisherTemplate(), now); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for reader := 0; reader < 16; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 50; index++ {
				value := publisher.Current()
				if value == nil {
					t.Error("descriptor disappeared during refresh")
					return
				}
				if _, err := Verify(value.Bytes(), now.Add(time.Second)); err != nil {
					t.Errorf("published descriptor is invalid: %v", err)
					return
				}
			}
		}()
	}
	for index := 0; index < 10; index++ {
		if _, err := publisher.Refresh(ctx, publisherTemplate(), now.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}
