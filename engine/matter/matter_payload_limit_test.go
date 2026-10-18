/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"testing"

	"brique_engine/circulation"
)

func TestMatterInlineMutationLimitAndHTTPAmbiguity(t *testing.T) {
	for _, operation := range []string{"write", "append"} {
		t.Run(operation, func(t *testing.T) {
			l, comm, _, _ := createAppendFixture(t, circulation.ValueFramingBytes, "base")
			l.inlineMaxBytes = 2
			invoke := func(params map[string]any) circulation.Message {
				message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: operation, Params: params}}
				if operation == "write" {
					l.capMatterWrite(message)
				} else {
					l.capMatterAppend(message)
				}
				return recvMatterLoopMsg(t, comm, operation)
			}
			tooLarge := invoke(map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: "abc"})
			if tooLarge.Response.Status != circulation.ValueStatusError || tooLarge.Response.Error == nil || tooLarge.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonPayloadTooLarge {
				t.Fatalf("large inline %s was not refused: %#v", operation, tooLarge)
			}
			ambiguous := invoke(map[string]any{circulation.KeyMatterID: "log", circulation.KeyData: "x", circulation.KeyHTTPData: true})
			if ambiguous.Response.Status != circulation.ValueStatusError || ambiguous.Response.Error == nil || ambiguous.Response.Error.Code != circulation.ValueCodeInvalid {
				t.Fatalf("ambiguous %s was not invalid: %#v", operation, ambiguous)
			}
		})
	}
}

func TestMatterCreateAndDeriveInlinePayloadLimit(t *testing.T) {
	for _, operation := range []string{"create", "derive"} {
		t.Run(operation, func(t *testing.T) {
			l, comm, _ := newMatterLoopHarness(t)
			l.inlineMaxBytes = 2
			params := map[string]any{
				circulation.KeyMatter:  map[string]any{circulation.KeyBrique: map[string]any{circulation.KeySubstanceMode: circulation.ValueModeBrique}},
				circulation.KeyPayload: "abc",
			}
			message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{IntentionID: operation, Params: params}}
			if operation == "create" {
				params[circulation.KeyMatterID] = "created"
				l.capMatterCreate(message)
			} else {
				params[circulation.KeyTargetMatterID] = "derived"
				l.capMatterDerive(message)
			}
			response := recvMatterLoopMsg(t, comm, operation)
			if response.Response.Status != circulation.ValueStatusError || response.Response.Error == nil || response.Response.Error.Details[circulation.KeyReason] != circulation.ValueReasonPayloadTooLarge {
				t.Fatalf("large %s payload was not refused: %#v", operation, response)
			}
		})
	}
}
