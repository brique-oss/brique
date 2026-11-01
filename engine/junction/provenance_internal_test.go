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

package junction

import (
	"fmt"
	"testing"
	"time"
)

func TestProvenanceBookIsBoundedAndForgetsExpiredRoots(t *testing.T) {
	book := newProvenanceBook()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	book.now = func() time.Time { return now }
	for i := 0; i < provenanceMaxEntries+500; i++ {
		book.Record(fmt.Sprintf("root-%d", i), "p", "")
	}
	if len(book.entries) > provenanceMaxEntries {
		t.Fatalf("the book grew to %d entries", len(book.entries))
	}
	if _, _, ok := book.Lookup(fmt.Sprintf("root-%d", provenanceMaxEntries+499)); !ok {
		t.Fatal("the newest root was not recorded")
	}
	now = now.Add(provenanceLifetime + time.Second)
	if _, _, ok := book.Lookup(fmt.Sprintf("root-%d", provenanceMaxEntries+499)); ok {
		t.Fatal("an expired root is still known")
	}
	book.Record("", "p", "")
	book.Record("x", "", "")
	if _, _, ok := book.Lookup("x"); ok {
		t.Fatal("an entry without a principal was recorded")
	}
}
