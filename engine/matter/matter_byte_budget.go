/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package matter

import (
	"strings"
	"sync"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/admission"
)

// Per-principal byte budget (lot B, D-17/D-18). It is a net against a loop or a
// bug on the side of a known peer, so its defaults are large; it does not stand
// in for the strict budgets a public mutation would need. Local callers are not
// metered. The budget lives on the instance-wide data plane so that a principal
// cannot multiply it by spreading its writes over several Contexts.
const (
	defaultOwnerBytesBurst     int64 = 2048 << 20
	defaultOwnerBytesPerMinute int64 = 1024 << 20
	maxByteBudgetOwners              = 4096

	// Heavy operations (create, write, append, clone, derive, and so the opening of
	// a lease) draw on a second bucket. Its default equals the general request rate of a
	// principal that calls over the network (20/s, burst 40): it is a net against a loop,
	// and it must never throttle an editor that saves often or an agent that writes in
	// bursts. The plan's tenth of that rate (2/s, burst 5) is to be adopted only once
	// measured; an operator can already lower it with heavy_per_second / heavy_burst.
	defaultHeavyPerSecond int64 = 20
	defaultHeavyBurst     int64 = 40
)

type byteBucket struct {
	tokens float64
	at     time.Time
}

type ownerByteBudget struct {
	mu        sync.Mutex
	burst     float64
	perSecond float64
	now       func() time.Time
	buckets   map[string]*byteBucket
}

func newOwnerByteBudget(burst, perMinute int64) *ownerByteBudget {
	if burst <= 0 {
		return nil
	}
	return &ownerByteBudget{burst: float64(burst), perSecond: float64(perMinute) / 60, now: time.Now,
		buckets: map[string]*byteBucket{}}
}

// take reserves n bytes for owner, or refuses without consuming anything.
func (b *ownerByteBudget) take(owner string, n int64) bool {
	if b == nil || n <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	bucket := b.buckets[owner]
	if bucket == nil {
		if len(b.buckets) >= maxByteBudgetOwners {
			b.pruneLocked(now)
		}
		bucket = &byteBucket{tokens: b.burst, at: now}
		b.buckets[owner] = bucket
	}
	bucket.tokens += now.Sub(bucket.at).Seconds() * b.perSecond
	if bucket.tokens > b.burst {
		bucket.tokens = b.burst
	}
	bucket.at = now
	if float64(n) > bucket.tokens {
		return false
	}
	bucket.tokens -= float64(n)
	return true
}

// pruneLocked forgets owners whose bucket has refilled completely: dropping them
// is indistinguishable from keeping them.
func (b *ownerByteBudget) pruneLocked(now time.Time) {
	for owner, bucket := range b.buckets {
		if bucket.tokens+now.Sub(bucket.at).Seconds()*b.perSecond >= b.burst {
			delete(b.buckets, owner)
		}
	}
}

func byteBudgetOwner(admitted *admission.Context) string {
	if admitted == nil {
		return ""
	}
	return "principal\x00" + admitted.PrincipalID()
}

// refuseWhenByteBudgetExceeded charges n bytes to the external caller of in and
// answers a stable refusal when its budget is spent. It returns true when the
// mutation must not proceed.
func (l *MatterLoop) refuseWhenByteBudgetExceeded(in circulation.Intention, n int64) bool {
	if n <= 0 || l == nil || l.subHTTP == nil || l.subHTTP.byteBudget == nil {
		return false
	}
	delivery, external := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !external {
		return false
	}
	admitted, isExternal := delivery.ExternalAdmission()
	if !isExternal || admitted == nil {
		return false
	}
	if l.subHTTP.byteBudget.take(byteBudgetOwner(admitted), n) {
		return false
	}
	l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
		map[string]any{circulation.KeyReason: circulation.ValueReasonByteBudgetExceeded}, "byte budget of the caller is spent"))
	return true
}

// chargeLeaseBytes charges an uploaded body to the principal that opened its lease.
func (g *HTTPSubstanceGetter) chargeLeaseBytes(lz *lease, n int64) bool {
	if g == nil || g.byteBudget == nil || lz == nil || lz.authz == nil || lz.authz.admitted == nil {
		return true
	}
	return g.byteBudget.take(byteBudgetOwner(lz.authz.admitted), n)
}

// refuseWhenHeavyBudgetExceeded counts one heavy operation against the external
// caller of in. The bucket is the same mechanism as the byte budget, counting
// operations instead of bytes.
func (l *MatterLoop) refuseWhenHeavyBudgetExceeded(in circulation.Intention) bool {
	if l == nil || l.subHTTP == nil || l.subHTTP.heavyBudget == nil {
		return false
	}
	delivery, external := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !external {
		return false
	}
	admitted, isExternal := delivery.ExternalAdmission()
	if !isExternal || admitted == nil {
		return false
	}
	if l.subHTTP.heavyBudget.take(byteBudgetOwner(admitted), 1) {
		return false
	}
	l.emitResponseError(errorResp(in, circulation.ValueCodeRefused,
		map[string]any{circulation.KeyReason: circulation.ValueReasonRateLimited}, "heavy operation rate of the caller exceeded"))
	return true
}
