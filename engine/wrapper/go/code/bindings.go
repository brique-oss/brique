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

import "brique/wrapper/wrapper"

// BuildBindings declares the static capacity and matter bindings for this wrapper.
// Replace the example capacity with your own implementations.
func BuildBindings(_ *wrapper.Runtime) wrapper.BindingsFragment {
	return wrapper.BindingsFragment{
		Capacities: []wrapper.CapacityBinding{
			{
				Context: "",
				Name:    "go.echo",
				Handler: func(params wrapper.Params) (any, error) {
					return map[string]any{"echo": params.String("message")}, nil
				},
			},
		},
		Matters:  []wrapper.MatterBinding{},
		Shutdown: []func(){},
	}
}
