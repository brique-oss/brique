/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package matter

import (
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
)

func TestOwnerByteBudgetRefillsAndNeverOverdraws(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	budget := newOwnerByteBudget(1000, 600) // 10 bytes per second
	budget.now = func() time.Time { return now }
	if !budget.take("a", 1000) {
		t.Fatal("a fresh owner must hold the whole burst")
	}
	if budget.take("a", 1) {
		t.Fatal("a spent budget accepted a byte")
	}
	if !budget.take("b", 1000) {
		t.Fatal("owners must not share a budget")
	}
	now = now.Add(5 * time.Second)
	if budget.take("a", 51) || !budget.take("a", 50) {
		t.Fatal("refill is not 10 bytes per second")
	}
	now = now.Add(time.Hour)
	if budget.take("a", 1001) || !budget.take("a", 1000) {
		t.Fatal("refill must stop at the burst")
	}
	if newOwnerByteBudget(0, 600) != nil || !(*ownerByteBudget)(nil).take("x", 1<<40) {
		t.Fatal("a disabled budget must not meter")
	}
}

func TestOwnerByteBudgetForgetsRefilledOwnersWhenFull(t *testing.T) {
	now := time.Now()
	budget := newOwnerByteBudget(100, 6000)
	budget.now = func() time.Time { return now }
	for index := 0; index < maxByteBudgetOwners; index++ {
		budget.take(string(rune('a'+index%26))+time.Duration(index).String(), 1)
	}
	now = now.Add(time.Minute)
	budget.take("newcomer", 1)
	if len(budget.buckets) > maxByteBudgetOwners {
		t.Fatalf("%d owners tracked", len(budget.buckets))
	}
}

func TestByteBudgetConfiguration(t *testing.T) {
	if budget := NewMatterLoopWithConfig(nil, map[string]any{}).subHTTP.byteBudget; budget == nil || budget.burst != float64(defaultOwnerBytesBurst) {
		t.Fatalf("default budget = %+v", budget)
	}
	if NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterOwnerBytesBurstMiB: int64(0)}).subHTTP.byteBudget != nil {
		t.Fatal("an explicit zero burst must disable the budget")
	}
	budget := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterOwnerBytesBurstMiB: int64(3),
		configuration.KeyMatterOwnerBytesPerMinuteMiB: int64(6)}).subHTTP.byteBudget
	if budget.burst != float64(3<<20) || budget.perSecond != float64(6<<20)/60 {
		t.Fatalf("configured budget = %+v", budget)
	}
}

func TestExternalCallerIsMeteredAndLocalCallerIsNot(t *testing.T) {
	w := newSubscriptionWorld(t)
	w.loop.subHTTP.byteBudget = newOwnerByteBudget(8, 0)
	charge := func(bytes int64) func(circulation.Message) {
		return func(message circulation.Message) {
			if w.loop.refuseWhenByteBudgetExceeded(message.Intention, bytes) {
				return
			}
			w.loop.emitResponseError(errorResp(message.Intention, "ok_marker", nil, "charged"))
		}
	}
	if response := w.call(leaseTestID1, map[string]any{}, charge(8)); response.Error.Code != "ok_marker" {
		t.Fatalf("a charge inside the burst was refused: %+v", response)
	}
	response := w.call(leaseTestID1, map[string]any{}, charge(1))
	if response.Error.Code != circulation.ValueCodeRefused || response.Error.Details[circulation.KeyReason] != circulation.ValueReasonByteBudgetExceeded {
		t.Fatalf("a spent budget did not refuse: %+v", response)
	}
	// An Intention that did not come through an admitted door is a local caller.
	if w.loop.refuseWhenByteBudgetExceeded(circulation.Intention{IntentionID: "local-call"}, 1<<30) {
		t.Fatal("a local caller was metered")
	}
}

func TestLeaseUploadIsChargedToTheOpeningPrincipal(t *testing.T) {
	g := &HTTPSubstanceGetter{byteBudget: newOwnerByteBudget(10, 0)}
	if !g.chargeLeaseBytes(&lease{}, 1<<30) {
		t.Fatal("a lease without an external caller must not be metered")
	}
	var nilGetter *HTTPSubstanceGetter
	if !nilGetter.chargeLeaseBytes(&lease{}, 1<<30) {
		t.Fatal("a missing data plane must not meter")
	}
}

func TestHeavyOperationBudgetConfigurationAndMetering(t *testing.T) {
	defaults := NewMatterLoopWithConfig(nil, map[string]any{}).subHTTP.heavyBudget
	if defaults == nil || defaults.burst != 40 || defaults.perSecond != 20 {
		t.Fatalf("default heavy budget = %+v", defaults)
	}
	if NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterHeavyBurst: int64(0)}).subHTTP.heavyBudget != nil {
		t.Fatal("an explicit zero burst must disable the heavy budget")
	}
	tuned := NewMatterLoopWithConfig(nil, map[string]any{configuration.KeyMatterHeavyBurst: int64(9),
		configuration.KeyMatterHeavyPerSecond: int64(3)}).subHTTP.heavyBudget
	if tuned.burst != 9 || tuned.perSecond != 3 {
		t.Fatalf("tuned heavy budget = %+v", tuned)
	}

	w := newSubscriptionWorld(t)
	w.loop.subHTTP.heavyBudget = newOwnerByteBudget(2, 0)
	heavy := func(message circulation.Message) {
		if w.loop.refuseWhenHeavyBudgetExceeded(message.Intention) {
			return
		}
		w.loop.emitResponseError(errorResp(message.Intention, "ok_marker", nil, "admitted"))
	}
	for attempt := 0; attempt < 2; attempt++ {
		if response := w.call(leaseTestID1, map[string]any{}, heavy); response.Error.Code != "ok_marker" {
			t.Fatalf("heavy operation %d inside the burst was refused: %+v", attempt+1, response)
		}
	}
	response := w.call(leaseTestID1, map[string]any{}, heavy)
	if response.Error.Code != circulation.ValueCodeRefused || response.Error.Details[circulation.KeyReason] != circulation.ValueReasonRateLimited {
		t.Fatalf("the third heavy operation was not rate limited: %+v", response)
	}
	if w.loop.refuseWhenHeavyBudgetExceeded(circulation.Intention{IntentionID: "local-call"}) {
		t.Fatal("a local caller was metered")
	}
}
