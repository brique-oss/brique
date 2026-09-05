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

// Normalizes an OS-native filesystem path (which may use "\" on Windows) to
// forward-slash form, so downstream "/"-based concatenation and regex logic
// stays correct on every OS. Node filesystem APIs and vscode.Uri.file() both
// accept "/" paths on Windows, so no reverse conversion is needed on output.
export function toPosixPath(p: string): string {
  return p.replace(/\\/g, "/");
}
