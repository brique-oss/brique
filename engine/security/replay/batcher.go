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

package replay

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type BatchOptions struct {
	MaxBatch   int
	MaxDelay   time.Duration
	QueueDepth int
}

func DefaultBatchOptions() BatchOptions {
	return BatchOptions{MaxBatch: 64, MaxDelay: time.Millisecond, QueueDepth: 1024}
}

type batchCall struct {
	request reserveRequest
	result  chan reserveResult
}

type Batcher struct {
	ledger  *Ledger
	options BatchOptions
	queue   chan batchCall
	stop    chan struct{}
	done    chan struct{}

	mu     sync.Mutex
	closed bool
}

func NewBatcher(ledger *Ledger, options BatchOptions) (*Batcher, error) {
	if ledger == nil || options.MaxBatch <= 0 || options.MaxBatch > 4096 ||
		options.MaxDelay <= 0 || options.MaxDelay > 100*time.Millisecond || options.QueueDepth < options.MaxBatch {
		return nil, fmt.Errorf("invalid replay group-commit options")
	}
	batcher := &Batcher{
		ledger: ledger, options: options, queue: make(chan batchCall, options.QueueDepth),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go batcher.loop()
	return batcher, nil
}

func (batcher *Batcher) Reserve(ctx context.Context, key Key, requestHash string, expiresAt, retainUntil time.Time) (Reservation, error) {
	if ctx == nil {
		return Reservation{}, fmt.Errorf("reservation context is required")
	}
	call := batchCall{
		request: reserveRequest{key: key, requestHash: requestHash, expiresAt: expiresAt, retainUntil: retainUntil},
		result:  make(chan reserveResult, 1),
	}
	batcher.mu.Lock()
	if batcher.closed {
		batcher.mu.Unlock()
		return Reservation{}, ErrClosed
	}
	select {
	case batcher.queue <- call:
		batcher.mu.Unlock()
	case <-ctx.Done():
		batcher.mu.Unlock()
		return Reservation{}, ctx.Err()
	}
	select {
	case result := <-call.result:
		return result.reservation, result.err
	case <-ctx.Done():
		// The accepted reservation may still become durable. Cancellation is
		// therefore never reported as proof that no reservation exists.
		return Reservation{}, ctx.Err()
	}
}

func (batcher *Batcher) Close() error {
	batcher.mu.Lock()
	if batcher.closed {
		batcher.mu.Unlock()
		<-batcher.done
		return nil
	}
	batcher.closed = true
	close(batcher.stop)
	batcher.mu.Unlock()
	<-batcher.done
	return nil
}

func (batcher *Batcher) loop() {
	defer close(batcher.done)
	for {
		select {
		case first := <-batcher.queue:
			batcher.collectAndCommit(first)
		case <-batcher.stop:
			batcher.drainAndCommit()
			return
		}
	}
}

func (batcher *Batcher) collectAndCommit(first batchCall) {
	calls := make([]batchCall, 0, batcher.options.MaxBatch)
	calls = append(calls, first)
	timer := time.NewTimer(batcher.options.MaxDelay)
	defer timer.Stop()
	for len(calls) < batcher.options.MaxBatch {
		select {
		case call := <-batcher.queue:
			calls = append(calls, call)
		case <-timer.C:
			batcher.commit(calls)
			return
		case <-batcher.stop:
			batcher.commit(calls)
			batcher.drainAndCommit()
			return
		}
	}
	batcher.commit(calls)
}

func (batcher *Batcher) drainAndCommit() {
	for {
		calls := make([]batchCall, 0, batcher.options.MaxBatch)
		for len(calls) < batcher.options.MaxBatch {
			select {
			case call := <-batcher.queue:
				calls = append(calls, call)
			default:
				if len(calls) > 0 {
					batcher.commit(calls)
				}
				return
			}
		}
		batcher.commit(calls)
	}
}

func (batcher *Batcher) commit(calls []batchCall) {
	requests := make([]reserveRequest, len(calls))
	for index := range calls {
		requests[index] = calls[index].request
	}
	results := batcher.ledger.reserveBatch(requests)
	for index := range calls {
		calls[index].result <- results[index]
	}
}
