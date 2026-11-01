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
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// A family has one route in: its channel of Delivery. A bare message channel used as
// an inbox would be handled as trusted local traffic with no authority behind it
// (SECURITY.md §1.3), so no family loop may own one.
func TestNoFamilyLoopOwnsABareMessageInbox(t *testing.T) {
	for _, family := range []string{"execution", "matter", "reflexive", "trace", "communication", "context"} {
		files, err := filepath.Glob(filepath.Join("..", family, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no sources for %s: %v", family, err)
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(parsed, func(n ast.Node) bool {
				field, ok := n.(*ast.Field)
				if !ok {
					return true
				}
				channel, ok := field.Type.(*ast.ChanType)
				if !ok {
					return true
				}
				selector, ok := channel.Value.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Message" {
					return true
				}
				for _, ident := range field.Names {
					switch ident.Name {
					case "in", "inbox", "input", "ingress":
						t.Errorf("%s: %s owns a bare message channel", name, ident.Name)
					}
				}
				return true
			})
		}
	}
}
