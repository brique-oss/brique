/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package instancedescriptor

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"

	"brique_engine/security/signer"
)

type SignerSource func() signer.Signer

type PublicationTemplate struct {
	InstanceID         string
	AudienceInstanceID string
	UIOrigins          []string
	Endpoints          []Endpoint
	Protocols          []string
}

type PublisherOptions struct {
	Signer        SignerSource
	Lifetime      time.Duration
	RefreshBefore time.Duration
}

// Publisher owns one atomic in-memory descriptor. It has no generation
// counter or durable state; a process restart simply signs a fresh document.
type Publisher struct {
	source        SignerSource
	lifetime      time.Duration
	refreshBefore time.Duration

	refreshMu sync.Mutex
	mu        sync.RWMutex
	current   *Verified
	template  PublicationTemplate
}

func NewPublisher(options PublisherOptions) (*Publisher, error) {
	if options.Lifetime == 0 {
		options.Lifetime = 2 * time.Minute
	}
	if options.RefreshBefore == 0 {
		options.RefreshBefore = 30 * time.Second
	}
	if options.Signer == nil || options.Lifetime <= 0 || options.Lifetime > MaxLifetime ||
		options.RefreshBefore <= 0 || options.RefreshBefore >= options.Lifetime {
		return nil, ErrInvalid
	}
	return &Publisher{source: options.Signer, lifetime: options.Lifetime, refreshBefore: options.RefreshBefore}, nil
}

// EnsureFresh returns the current descriptor when its key, configuration and
// remaining lifetime still match. Otherwise it signs and atomically publishes
// a replacement. A failed replacement leaves the last valid value untouched.
func (publisher *Publisher) EnsureFresh(ctx context.Context, template PublicationTemplate, now time.Time) (*Verified, error) {
	if publisher == nil || now.IsZero() {
		return nil, ErrInvalid
	}
	template = normalizeTemplate(template)
	publisher.refreshMu.Lock()
	defer publisher.refreshMu.Unlock()
	current := publisher.Current()
	active := publisher.source()
	if !validPublisherSigner(active) {
		return nil, ErrInvalid
	}
	if current != nil && current.ExpiresAt().Sub(now) > publisher.refreshBefore &&
		current.Protected().KeyID == active.Descriptor().KeyID && publisher.sameTemplate(template) {
		if verified, err := Verify(current.Bytes(), now); err == nil {
			return verified, nil
		}
	}
	return publisher.publish(ctx, active, template, now)
}

// Refresh always signs a replacement, for an immediate origin, endpoint or
// protocol withdrawal. It does not clear the previous value on failure.
func (publisher *Publisher) Refresh(ctx context.Context, template PublicationTemplate, now time.Time) (*Verified, error) {
	if publisher == nil || now.IsZero() {
		return nil, ErrInvalid
	}
	template = normalizeTemplate(template)
	publisher.refreshMu.Lock()
	defer publisher.refreshMu.Unlock()
	active := publisher.source()
	if !validPublisherSigner(active) {
		return nil, ErrInvalid
	}
	return publisher.publish(ctx, active, template, now)
}

func (publisher *Publisher) publish(ctx context.Context, active signer.Signer, template PublicationTemplate, now time.Time) (*Verified, error) {
	value, err := Build(ctx, active, BuildRequest{InstanceID: template.InstanceID,
		AudienceInstanceID: template.AudienceInstanceID, UIOrigins: template.UIOrigins,
		Endpoints: template.Endpoints, Protocols: template.Protocols, IssuedAt: now, ExpiresAt: now.Add(publisher.lifetime)})
	if err != nil {
		return nil, err
	}
	publisher.mu.Lock()
	publisher.current = value
	publisher.template = cloneTemplate(template)
	publisher.mu.Unlock()
	return value, nil
}

// Current implements browserhello.DescriptorSource when used as publisher.Current.
func (publisher *Publisher) Current() *Verified {
	if publisher == nil {
		return nil
	}
	publisher.mu.RLock()
	defer publisher.mu.RUnlock()
	return publisher.current
}

func (publisher *Publisher) Clear() {
	if publisher == nil {
		return
	}
	publisher.refreshMu.Lock()
	defer publisher.refreshMu.Unlock()
	publisher.mu.Lock()
	publisher.current = nil
	publisher.template = PublicationTemplate{}
	publisher.mu.Unlock()
}

func (publisher *Publisher) sameTemplate(template PublicationTemplate) bool {
	publisher.mu.RLock()
	defer publisher.mu.RUnlock()
	return reflect.DeepEqual(publisher.template, template)
}

func normalizeTemplate(value PublicationTemplate) PublicationTemplate {
	value = cloneTemplate(value)
	sort.Strings(value.UIOrigins)
	sort.Slice(value.Endpoints, func(i, j int) bool {
		if value.Endpoints[i].Name == value.Endpoints[j].Name {
			return value.Endpoints[i].URL < value.Endpoints[j].URL
		}
		return value.Endpoints[i].Name < value.Endpoints[j].Name
	})
	sort.Strings(value.Protocols)
	return value
}

func cloneTemplate(value PublicationTemplate) PublicationTemplate {
	value.UIOrigins = append([]string(nil), value.UIOrigins...)
	value.Endpoints = append([]Endpoint(nil), value.Endpoints...)
	value.Protocols = append([]string(nil), value.Protocols...)
	return value
}

func validPublisherSigner(value signer.Signer) bool {
	if value == nil {
		return false
	}
	descriptor := value.Descriptor()
	return descriptor.KeyID != "" && descriptor.Algorithm == signer.AlgorithmEd25519
}
