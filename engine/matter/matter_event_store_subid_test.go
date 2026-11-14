package matter

import (
	"testing"

	"brique_engine/circulation"
)

func TestWithSubscriptionIDAddsIDWithoutModifyingSharedParams(t *testing.T) {
	shared := map[string]any{circulation.KeyMatterID: "m1"}
	a := withSubscriptionID(shared, "sub-a")
	b := withSubscriptionID(shared, "sub-b")
	if a[circulation.KeySubID] != "sub-a" || b[circulation.KeySubID] != "sub-b" {
		t.Fatalf("each event must carry its own subscription id: %v %v", a, b)
	}
	if _, polluted := shared[circulation.KeySubID]; polluted {
		t.Fatalf("shared params were modified: %v", shared)
	}
	if got := withSubscriptionID(shared, "")[circulation.KeySubID]; got != nil {
		t.Fatalf("empty id must not be added, got %v", got)
	}
}
