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

// Package policysrc turns a short, human-written access source into the signed
// trust-policy document the engine enforces (SECURITY.md §3). It changes nothing at run time.
//
// The source has principals, each with grants (read, write, create, call, define, control
// over targets, with except lists), optional actors and, for a local principal, no keys.
// The compiler emits a format-2 policy (a verb layer and a resource layer, no capacity name
// anywhere) and format 3 only when a grant uses as_owner. It needs no capacity catalog, only
// the resource actions of each kind; every permission carries an origin so that a decision can
// be traced to the grant that produced it.
//
// reference.go is a deliberately naive evaluator written from the specification and
// independent of the compiler: the property tests check that the compiled snapshot gives the
// same answer as this oracle on random sources, and the owner oracle checks which owner a
// call runs as.
package policysrc
