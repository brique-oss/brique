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

package comm

import (
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"brique_engine/engine/configuration"
	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
)

// Two goroutines route every message of the communication family, for every interface. An
// interface whose consumer no longer reads must therefore not be waited for without an end:
// it would stop the routing of the others. A routing loop offers a message to the queue of an
// interface and waits at most egressWait for a place; past it, the interface is given up as too
// slow and its connection is closed, as the read side does with ingress_wait_ms. The messages
// waiting for an answer on that connection are answered `connection_lost`.

const (
	// defaultEgressBuffer: places of the queue in front of the writer of an interface. It is not
	// a throughput setting: a larger queue only moves the moment the sender is held back.
	defaultEgressBuffer = 16
	// defaultEgressWait is the wait of a routing loop for a place; the read side waits as long.
	defaultEgressWait = 5 * time.Second
)

// egressSettings resolves the queue size and the wait of one interface: its own config first,
// then the communication config, then the defaults.
func egressSettings(cfg CommCfg, ic InterfaceCfg) (places int, wait time.Duration) {
	places = defaultEgressBuffer
	if cfg.EgressBuffer > 0 {
		places = cfg.EgressBuffer
	}
	wait = defaultEgressWait
	if cfg.EgressWaitMs > 0 {
		wait = time.Duration(cfg.EgressWaitMs) * time.Millisecond
	}
	if v := shared.AnyToInt64(ic.Config[configuration.KeyEgressBuffer]); v > 0 {
		places = int(v)
	}
	if v := shared.AnyToInt64(ic.Config[configuration.KeyEgressWaitMs]); v > 0 {
		wait = time.Duration(v) * time.Millisecond
	}
	return places, wait
}

type offerOutcome uint8

const (
	offerAccepted offerOutcome = iota
	// offerTimedOut: no place came within the wait.
	offerTimedOut
	// offerStopped: the loop was stopped while waiting.
	offerStopped
)

// offer puts item in the queue. With a place free it does not wait and starts no timer. Otherwise
// it waits at most wait (0 = defaultEgressWait) for a place. It returns how long it waited.
func offer[T any](queue chan<- T, item T, wait time.Duration, done <-chan struct{}) (offerOutcome, time.Duration) {
	select {
	case queue <- item:
		return offerAccepted, 0
	default:
	}
	if wait <= 0 {
		wait = defaultEgressWait
	}
	started := time.Now()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case queue <- item:
		return offerAccepted, time.Since(started)
	case <-timer.C:
		return offerTimedOut, time.Since(started)
	case <-done:
		return offerStopped, time.Since(started)
	}
}

// offerSignedEgress puts a signed message in the queue in front of the signed writer of an interface.
// Like every other egress queue it holds the routing loop back for at most the wait of the interface
// instead of dropping the message at the first full queue: a burst of answers (a peer refused for its
// rate answers every call at once) is a normal load, not a reason to lose answers its callers wait for.
func (l *CommLoop) offerSignedEgress(runtime *InterfaceRuntime, item signedExternalEgressItem) offerOutcome {
	outcome, waited := offer(runtime.SignedExternalEgress, item, runtime.egressWait, l.done)
	runtime.egress.note(outcome, waited)
	return outcome
}

// egressCounters say what the routing loops met in front of one interface: how often its queue
// was full when a message came, how long they waited for a place, and how often they gave up.
// They answer the question "is the queue too small?" with a measure instead of a guess.
type egressCounters struct {
	offers   atomic.Uint64 // messages offered to the queue
	full     atomic.Uint64 // offers that found the queue full
	timeouts atomic.Uint64 // offers that gave up
	waitedNs atomic.Int64  // total time waited for a place
	maxWait  atomic.Int64  // longest wait for a place, in ns
}

// EgressStats is a copy of the counters of one interface.
type EgressStats struct {
	Offers   uint64
	Full     uint64
	Timeouts uint64
	Waited   time.Duration
	MaxWait  time.Duration
}

func (c *egressCounters) note(outcome offerOutcome, waited time.Duration) {
	c.offers.Add(1)
	if waited <= 0 && outcome == offerAccepted {
		return
	}
	c.full.Add(1)
	c.waitedNs.Add(int64(waited))
	for {
		current := c.maxWait.Load()
		if int64(waited) <= current || c.maxWait.CompareAndSwap(current, int64(waited)) {
			break
		}
	}
	if outcome == offerTimedOut {
		c.timeouts.Add(1)
	}
}

func (c *egressCounters) snapshot() EgressStats {
	return EgressStats{
		Offers: c.offers.Load(), Full: c.full.Load(), Timeouts: c.timeouts.Load(),
		Waited: time.Duration(c.waitedNs.Load()), MaxWait: time.Duration(c.maxWait.Load()),
	}
}

// EgressStats returns the counters of every interface, by name.
func (l *CommLoop) EgressStats() map[string]EgressStats {
	out := make(map[string]EgressStats, len(l.ifaces))
	for name, rt := range l.ifaces {
		if rt != nil {
			out[name] = rt.egress.snapshot()
		}
	}
	return out
}

// InterfaceEgress reports the egress queue of every interface, sorted by name: its size, how full it is now, and
// the counters. It is read by read.state; the queue lengths are a snapshot taken without stopping anything.
func (l *CommLoop) InterfaceEgress() []junction.InterfaceEgress {
	if l == nil {
		return nil
	}
	names := make([]string, 0, len(l.ifaces))
	for name, rt := range l.ifaces {
		if rt != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]junction.InterfaceEgress, 0, len(names))
	for _, name := range names {
		rt := l.ifaces[name]
		stats := rt.egress.snapshot()
		out = append(out, junction.InterfaceEgress{
			Name: name, Type: rt.Type, Status: rt.State().Status.String(),
			Places: cap(rt.Egress), Queued: len(rt.Egress),
			Offers: stats.Offers, Full: stats.Full, Timeouts: stats.Timeouts,
			WaitedNs: int64(stats.Waited), MaxWaitNs: int64(stats.MaxWait),
		})
	}
	return out
}

// interfaceDisconnecter is implemented by an interface that can close the connection of its
// consumer. It must not block on that consumer.
type interfaceDisconnecter interface {
	Disconnect(reason string)
}

// giveUpSlowInterface closes the connection of an interface whose queue stayed full for
// its whole wait, once. The close runs apart from the routing loop: it answers `connection_lost`
// to the awaited messages through the outcome queue and must not hold the loop.
func (l *CommLoop) giveUpSlowInterface(name string, rt *InterfaceRuntime, waited time.Duration) {
	reason := fmt.Sprintf("interface %s did not take a message in %s (its queue of %d places stayed full): connection closed", name, waited.Round(time.Millisecond), cap(rt.Egress))
	if rt.Impl == nil {
		return
	}
	disconnecter, ok := rt.Impl.(interfaceDisconnecter)
	if !ok || !rt.disconnecting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer rt.disconnecting.Store(false)
		defer shared.Recover(l.tracePanic)
		disconnecter.Disconnect(reason)
	}()
}
