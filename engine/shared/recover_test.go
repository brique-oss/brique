package shared

import (
	"strings"
	"testing"
)

func TestRecoverHandlesAPanicAndTheCallerCarriesOn(t *testing.T) {
	var got string
	func() {
		defer Recover(func(text string) { got = text })
		panic("boom")
	}()
	if !strings.Contains(got, "panic: boom") {
		t.Fatalf("description = %q", got)
	}
	if !strings.Contains(got, "recover_test.go") {
		t.Fatalf("the description should point at the code that panicked: %q", got)
	}
}

func TestRecoverDoesNothingWithoutPanic(t *testing.T) {
	called := false
	func() {
		defer Recover(func(string) { called = true })
	}()
	if called {
		t.Fatal("the handler ran without a panic")
	}
}

func TestRecoverOnARuntimeError(t *testing.T) {
	var got string
	func() {
		defer Recover(func(text string) { got = text })
		var m map[string]int
		m["x"] = 1 // assignment to a nil map
	}()
	if !strings.Contains(got, "nil map") {
		t.Fatalf("description = %q", got)
	}
}
