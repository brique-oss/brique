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

import "brique_engine/circulation"

func (l *CommLoop) prepareInterfaceEgress(msg circulation.Message) (circulation.Message, error) {
	wireMsg := rewriteExternalWireDestination(msg)
	toStr := string(msgToContext(&msg))
	if len(toStr) > 0 && toStr[0] == '@' {
		tag, _, ok := parseAtAddress(toStr)
		if ok && len(tag) > 4 && tag[:4] == "ext_" {
			if err := l.updateIdentityAndSign(&wireMsg); err != nil {
				return circulation.Message{}, err
			}
		}
	}
	return wireMsg, nil
}
