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

package matter

import (
	"strings"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
)

// externalReadScope returns the read scope of the external caller being served,
// or nil for a local caller.
func (l *MatterLoop) externalReadScope(in circulation.Intention) *admission.ReadScope {
	delivery, bound := l.externalRequest(strings.TrimSpace(in.IntentionID))
	if !bound {
		return nil
	}
	admitted, external := delivery.ExternalAdmission()
	if !external || admitted == nil {
		return nil
	}
	if l.frame != nil && l.frame.CtxCommReg != nil {
		if provider, ok := l.frame.CtxCommReg.(junction.TrustRuntimeProvider); ok {
			return admitted.ReadScope(provider.TrustRuntime())
		}
	}
	return admitted.ReadScope(nil) // reads nothing
}
