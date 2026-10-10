package shared

import "testing"

func TestValidateControlMessageBytes(t *testing.T) {
	if err := ValidateControlMessageBytes(16, 16); err != nil {
		t.Fatalf("message at limit rejected: %v", err)
	}
	if err := ValidateControlMessageBytes(17, 16); err == nil {
		t.Fatal("message above limit should be rejected")
	}
	if err := ValidateControlMessageBytes(DefaultControlMessageBytes+1, 0); err == nil {
		t.Fatal("default limit should apply when configured limit is absent")
	}
	if got := EffectiveControlMessageLimit(DefaultControlMessageBytes * 2); got != DefaultControlMessageBytes {
		t.Fatalf("configured limit raised global ceiling: %d", got)
	}
}
