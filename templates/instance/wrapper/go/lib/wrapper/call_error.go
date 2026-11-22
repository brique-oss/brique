// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import "errors"

// The errors of a call, typed by what a caller reacts to.
//
// An engine response with status error carries an error: a code, a message and details whose reason says what happened. A call returns it
// as a *CallError, and Kind says what a caller does about it. The reason decides first, the code when the reason says nothing known. The
// classification is shared by the three runtimes and fixed by vectors/errors.json.

// The kinds of error.
const (
	// KindBusy: the callee refused the call BEFORE running it because it is busy (the engine's Matter loop, the pending-work queue of a
	// wrapper): nothing was done, asking again is safe. The library already asked again; this is what is left when it stayed busy.
	KindBusy = "busy"
	// KindRevisionMismatch: the Matter changed since it was read; read it again.
	KindRevisionMismatch = "revision_mismatch"
	// KindLineCountMismatch: the journal has more lines than expected_line_count.
	KindLineCountMismatch = "line_count_mismatch"
	// KindConnectionLost: the connection of the callee dropped under an awaited call: the call may have been executed, never ask again blindly.
	KindConnectionLost = "connection_lost"
	KindNotFound       = "not_found"
	KindOther          = "other"
)

// CallError is the error response of a call: the code, the message and the details the callee gave.
type CallError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *CallError) Error() string { return e.Message }

// Reason is details.reason, "" when there is none.
func (e *CallError) Reason() string {
	reason, _ := e.Details["reason"].(string)
	return reason
}

// Kind says what a caller does about the error: one of the Kind constants.
func (e *CallError) Kind() string {
	switch e.Reason() {
	case "busy", "loop busy":
		return KindBusy
	case "line_count_mismatch":
		return KindLineCountMismatch
	case "revision_mismatch", "revision not coherent":
		return KindRevisionMismatch
	case "connection_lost":
		return KindConnectionLost
	}
	if e.Code == "not_found" {
		return KindNotFound
	}
	return KindOther
}

// OutcomeUnknown is true when the call may have been executed: asking again blindly could do it twice.
func (e *CallError) OutcomeUnknown() bool { return e.Kind() == KindConnectionLost }

// IsBusy reports whether the callee refused the call because it is busy (a *CallError of this kind is what is left when it stayed so).
func (e *CallError) IsBusy() bool { return e.Kind() == KindBusy }

// callErrorFrom is the *CallError of the error object of an engine response; a missing field has its default.
func callErrorFrom(object any) *CallError {
	failure := &CallError{Code: "internal", Message: "engine error"}
	record, ok := object.(map[string]any)
	if !ok {
		return failure
	}
	if code, _ := record["code"].(string); code != "" {
		failure.Code = code
	}
	if message, _ := record["message"].(string); message != "" {
		failure.Message = message
	}
	failure.Details, _ = record["details"].(map[string]any)
	return failure
}

func errorKindIs(err error, kinds ...string) bool {
	var failure *CallError
	if !errors.As(err, &failure) {
		return false
	}
	for _, want := range kinds {
		if failure.Kind() == want {
			return true
		}
	}
	return false
}

// IsRevisionMismatch: the Matter changed since it was read (a line count mismatch is one too).
func IsRevisionMismatch(err error) bool {
	return errorKindIs(err, KindRevisionMismatch, KindLineCountMismatch)
}

// IsLineCountMismatch: the journal has more lines than expected_line_count.
func IsLineCountMismatch(err error) bool { return errorKindIs(err, KindLineCountMismatch) }

// IsBusy: the engine refused the call before running it because it is busy, and stayed so after the attempts.
func IsBusy(err error) bool { return errorKindIs(err, KindBusy) }

// IsNotFound: the Matter or the capacity does not exist.
func IsNotFound(err error) bool { return errorKindIs(err, KindNotFound) }

// IsOutcomeUnknown: the call may have been executed.
func IsOutcomeUnknown(err error) bool {
	var failure *CallError
	return errors.As(err, &failure) && failure.OutcomeUnknown()
}
