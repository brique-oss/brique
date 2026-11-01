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

package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"brique_engine/shared"
)

// A ledger only grows: every transition appends a record and nothing leaves the index
// until a compaction drops it. Without maintenance an instance fills its entry limit
// (100 000 by default) or its journal limit (512 MiB) and then refuses every signed
// request until it is restarted by hand, which is a denial of service reachable by
// any authenticated peer. Maintenance is therefore part of the service, not an option.

// DefaultMaintenanceInterval is how often the service checks whether to compact.
const DefaultMaintenanceInterval = time.Minute

// minDroppableForCompaction avoids rewriting the journal for a handful of entries.
const minDroppableForCompaction = 1024

// compactionDue decides, from the footprint of the ledger, whether a compaction is
// worth its cost (the ledger is locked while it runs, about 0.2 s at 100 000 entries):
//   - enough entries could be dropped to be worth a rewrite, or
//   - some could be dropped and the entry limit is close (80 %), or
//   - the journal is close to its size limit (80 %) and has grown by a quarter since
//     the last pass: rewriting also folds the many records of an entry into one.
func compactionDue(entries, droppable int, journalBytes, lastJournalBytes int64, limits Limits) bool {
	switch {
	case droppable >= minDroppableForCompaction:
		return true
	case droppable > 0 && entries*5 >= limits.MaxEntries*4:
		return true
	case journalBytes*5 >= limits.MaxJournalBytes*4 && journalBytes >= lastJournalBytes+lastJournalBytes/4:
		return true
	}
	return false
}

// Maintain runs one maintenance pass on the journal stored at path and reports whether
// it compacted and how many entries it dropped. An interrupted pass leaves the old
// journal active; its leftover temporary file is removed by the next pass.
func (service *Service) Maintain(path string, now time.Time, last *int64) (compacted bool, dropped int, err error) {
	if service == nil || service.ledger == nil {
		return false, 0, ErrUnavailable
	}
	entries, droppable, size := service.ledger.Footprint(now)
	if !compactionDue(entries, droppable, size, *last, service.ledger.Limits()) {
		return false, 0, nil
	}
	temporary := path + ".compact"
	_ = os.Remove(temporary) // a leftover of an interrupted pass
	replacement, err := shared.OpenProtectedFile(temporary, true, nil, nil)
	if err != nil {
		return false, 0, fmt.Errorf("open replacement replay journal: %w", err)
	}
	dropped, err = service.ledger.CompactDropping(replacement, func() error {
		if err := shared.AtomicReplace(temporary, path); err != nil {
			return err
		}
		shared.SyncDir(filepath.Dir(path))
		return nil
	}, now)
	if err != nil {
		_ = replacement.Close()
		_ = os.Remove(temporary)
		return false, 0, err
	}
	_, _, *last = service.ledger.Footprint(now)
	return true, dropped, nil
}

// StartMaintenance runs a pass now and then every interval until stop is called (or the
// service is closed). report receives the outcome of a pass that failed; it may be nil.
func (service *Service) StartMaintenance(path string, interval time.Duration, now func() time.Time, report func(error)) {
	if service == nil || interval <= 0 {
		return
	}
	if now == nil {
		now = time.Now
	}
	stop := make(chan struct{})
	finished := make(chan struct{})
	var last int64
	pass := func() {
		if _, _, err := service.Maintain(path, now().UTC(), &last); err != nil && report != nil {
			report(err)
		}
	}
	pass()
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pass()
			}
		}
	}()
	var once sync.Once
	service.stopMaintenance = func() {
		once.Do(func() {
			close(stop)
			<-finished
		})
	}
}
