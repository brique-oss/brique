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

package trust

// EngineResourceActions is the closed set of resource actions the engine
// understands, per kind. It does not depend on the instance, so offline tools and
// tests can compile against it without importing the engine.
func EngineResourceActions() map[string][]string {
	return map[string][]string{
		"context":   {"control", "create", "delete", "read", "read_recursive", "stat", "write"},
		"matter":    {"append", "create", "delete", "read", "stat", "truncate", "unsubscribe", "write"},
		"structure": {"create", "delete", "read", "write"},
		"schema":    {"create", "delete", "read", "write"},
		"document":  {"create", "delete", "read", "write"},
		"capacity":  {"create", "delete", "execute", "read", "write"},
		"trace":     {"payloads", "read"},
	}
}
