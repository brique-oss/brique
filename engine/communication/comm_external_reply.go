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

package comm

import (
	"strings"

	"brique_engine/circulation"
)

func (l *CommLoop) rememberExternalReplyTarget(msg circulation.Message, publicFrom string, senderPub string) {
	if l == nil || publicFrom == "" || senderPub == "" || msg.Kind != circulation.ValueKindIntention {
		return
	}
	intentionID := msg.Intention.IntentionID
	if intentionID == "" {
		return
	}
	path := publicFrom
	if len(path) > 0 && path[0] == '/' {
		path = strings.TrimPrefix(path, "/")
	}
	l.externalReplyMu.Lock()
	l.externalReplyToPub[intentionID] = "@ext_" + senderPub + ":/" + path
	l.externalReplyMu.Unlock()
}

func (l *CommLoop) rewriteExternalResponseToPublicTarget(msg *circulation.Message) {
	if l == nil || msg == nil || msg.Kind != circulation.ValueKindResponse {
		return
	}
	intentionID := msg.Response.IntentionID
	if intentionID == "" {
		return
	}
	l.externalReplyMu.Lock()
	publicTo, ok := l.externalReplyToPub[intentionID]
	if ok {
		delete(l.externalReplyToPub, intentionID)
	}
	l.externalReplyMu.Unlock()
	if ok && publicTo != "" {
		msg.Response.To.Context = circulation.ContextID(publicTo)
	}
}
