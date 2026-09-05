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

package execution

import (
	"fmt"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

func (l *ExecutionLoop) invokeAndAwait(in circulation.Intention) (circulation.Response, error) {
	return l.invokeAndAwaitWithTimeout(in, 0)
}

func (l *ExecutionLoop) invokeAndAwaitWithTimeout(in circulation.Intention, timeout time.Duration) (circulation.Response, error) {
	in.IntentionID = stringsOrFallback(in.IntentionID, circulation.NewIntentionID())
	in.AwaitResponse = true

	if l.frame == nil {
		return errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonMissingContextFrame},
			"missing context frame",
		).Response, nil
	}
	if l.frame.FamIn == nil || l.frame.FamIn[shared.FamilyComm] == nil {
		return errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonCommFamilyNotAvailable},
			"communication family missing",
		).Response, nil
	}

	ch, ok := l.registerPending(in.IntentionID)
	if !ok {
		return errorResp(in,
			circulation.ValueCodeConfiguration,
			map[string]any{circulation.KeyReason: circulation.ValueReasonPendingRegisterError},
			"failed to register pending waiter",
		).Response, nil
	}
	defer l.unregisterPending(in.IntentionID)

	l.emitToComm(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: in,
	})

	if timeout <= 0 {
		timeout = l.timeoutFor(in)
	}
	msg, ok := l.awaitResponseChan(ch, timeout)
	if !ok {
		select {
		case <-l.done:
			return errorResp(in,
				circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonContextStopped},
				"Context has been stopped",
			).Response, nil
		default:
			return errorResp(in,
				circulation.ValueCodeRefused,
				map[string]any{circulation.KeyReason: circulation.ValueReasonTimeoutWaitingAnswer},
				"timeout waiting answer",
			).Response, nil
		}
	}
	if msg.Kind != circulation.ValueKindResponse {
		return circulation.Response{}, fmt.Errorf("unexpected awaited message kind: %s", msg.Kind)
	}
	return msg.Response, nil
}

func (l *ExecutionLoop) invokeNoWait(in circulation.Intention) error {
	in.IntentionID = stringsOrFallback(in.IntentionID, circulation.NewIntentionID())
	in.AwaitResponse = false

	if l.frame == nil {
		return fmt.Errorf("missing context frame")
	}
	if l.frame.FamIn == nil || l.frame.FamIn[shared.FamilyComm] == nil {
		return fmt.Errorf("communication family missing")
	}

	l.emitToComm(circulation.Message{
		Kind:      circulation.ValueKindIntention,
		Intention: in,
	})
	return nil
}

func stringsOrFallback(v string, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
