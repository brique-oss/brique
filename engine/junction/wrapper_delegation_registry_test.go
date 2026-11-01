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

package junction_test

import (
	"testing"

	"brique_engine/junction"
)

func TestContextCommRegistryOwnsOneStableWrapperDelegationStore(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	first := registry.WrapperDelegations()
	if first == nil || first != registry.WrapperDelegations() {
		t.Fatal("context registry did not retain one stable delegation store")
	}
	if (*junction.ContextCommRegistry)(nil).WrapperDelegations() != nil {
		t.Fatal("nil context registry exposed a delegation store")
	}
}
