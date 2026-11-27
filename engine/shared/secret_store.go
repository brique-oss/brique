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

package shared

// The values of the secrets a wrapper may be given live on the machine, one owner-only file
// each, under SecretsDir in the machine's security directory. brique-sec writes them and the
// engine reads them at the launch of a wrapper; both agree on these two values.
const (
	// SecretsDir is the folder of the values, relative to the machine's directory.
	SecretsDir = "secrets"
	// WrappersDir is the folder of the private directories of the wrappers that declare run.store,
	// relative to the machine's directory: <machine>/wrappers/<wrapper>.
	WrappersDir = "wrappers"
	// MaxSecretBytes bounds the value of one secret.
	MaxSecretBytes = 8 << 10
)
