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

package main

import (
	"fmt"
	"os"
	"strings"

	"brique/wrapper/wrapper"
)

func main() {
	rt := wrapper.NewRuntime()
	if err := rt.Run(BuildBindings); err != nil {
		s := err.Error()
		if strings.Contains(s, "connection refused") {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "wrapper fatal error: %v\n", err)
		os.Exit(1)
	}
}
