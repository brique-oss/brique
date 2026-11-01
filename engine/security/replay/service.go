/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package replay

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const MinimumRetentionAfterExpiry = 30 * time.Second

// Coordinator is the narrow durable replay contract consumed by the security
// runtime. It permits no deletion or journal replacement from Communication.
type Coordinator interface {
	Reserve(context.Context, Key, string, time.Time) (Reservation, error)
	MarkDispatched(Key, string) error
	RecordRunning(Key, string, []byte) error
	Complete(Key, string, []byte) error
	Reject(Key, string, []byte) error
	MarkOutcomeUnknown(Key, string) error
	Lookup(Key) (Entry, bool)
	Close() error
}

// Service combines bounded group-commit reservation with durable transitions.
// It owns both the batcher and ledger and closes them in that order.
type Service struct {
	ledger    *Ledger
	batcher   *Batcher
	retention time.Duration
	closeOnce sync.Once
	// stopMaintenance ends the background compaction, if one was started.
	stopMaintenance func()
	closeErr        error
}

func NewService(ledger *Ledger, batching BatchOptions, retentionAfterExpiry time.Duration) (*Service, error) {
	if ledger == nil || retentionAfterExpiry < MinimumRetentionAfterExpiry || retentionAfterExpiry > 30*24*time.Hour {
		return nil, fmt.Errorf("invalid replay service configuration")
	}
	batcher, err := NewBatcher(ledger, batching)
	if err != nil {
		return nil, err
	}
	return &Service{ledger: ledger, batcher: batcher, retention: retentionAfterExpiry}, nil
}

func (service *Service) Reserve(ctx context.Context, key Key, requestHash string, expiresAt time.Time) (Reservation, error) {
	if service == nil || service.batcher == nil {
		return Reservation{}, ErrUnavailable
	}
	return service.batcher.Reserve(ctx, key, requestHash, expiresAt, expiresAt.Add(service.retention))
}

func (service *Service) MarkDispatched(key Key, hash string) error {
	return service.ledger.MarkDispatched(key, hash)
}
func (service *Service) RecordRunning(key Key, hash string, envelope []byte) error {
	return service.ledger.RecordRunning(key, hash, envelope)
}
func (service *Service) Complete(key Key, hash string, envelope []byte) error {
	return service.ledger.Complete(key, hash, envelope)
}
func (service *Service) Reject(key Key, hash string, envelope []byte) error {
	return service.ledger.Reject(key, hash, envelope)
}
func (service *Service) MarkOutcomeUnknown(key Key, hash string) error {
	return service.ledger.MarkOutcomeUnknown(key, hash)
}
func (service *Service) Lookup(key Key) (Entry, bool) { return service.ledger.Lookup(key) }

func (service *Service) Close() error {
	if service == nil {
		return nil
	}
	service.closeOnce.Do(func() {
		if service.stopMaintenance != nil {
			service.stopMaintenance()
		}
		if service.batcher != nil {
			service.closeErr = service.batcher.Close()
		}
		if service.ledger != nil {
			if err := service.ledger.Close(); service.closeErr == nil {
				service.closeErr = err
			}
		}
	})
	return service.closeErr
}

var _ Coordinator = (*Service)(nil)
