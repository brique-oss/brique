package junction

import (
	"testing"

	"brique_engine/shared"
)

// SetFamily on a registry whose table was never created must not panic while
// holding the lock.
func TestSetFamilyOnAZeroRegistry(t *testing.T) {
	var r ContextRegistry
	ch := make(chan Delivery, 1)
	r.SetFamily(shared.FamilyComm, ch)
	if got, ok := r.LookupFamily(shared.FamilyComm); !ok || got == nil {
		t.Fatal("the family was not registered")
	}
	r.SetFamily(shared.FamilyComm, nil)
	if _, ok := r.LookupFamily(shared.FamilyComm); ok {
		t.Fatal("the family was not removed")
	}
}
