/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

type PublicError struct {
	Code    string
	Message string
	Reason  string
}

func newPublicError(code, message, reason string) *PublicError {
	return &PublicError{Code: code, Message: message, Reason: reason}
}

func (e *PublicError) Error() string { return e.Message }
