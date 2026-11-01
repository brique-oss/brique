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

package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Filter selects journal lines. Zero fields match everything.
type Filter struct {
	Since     time.Time
	Principal string
	Reason    string
	// Limit keeps the most recent N lines when positive.
	Limit int
}

const maxLineBytes = 8 << 10

// Read returns the matching lines of every journal file in date order. Lines it
// cannot parse (a torn write after a crash) are skipped, never fatal.
func Read(directory string, filter Filter) ([]Record, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if fileNamePattern.MatchString(entry.Name()) && entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var out []Record
	for _, name := range names {
		if filter.Since.After(time.Time{}) {
			day := fileNamePattern.FindStringSubmatch(name)[1]
			if day < filter.Since.UTC().Format("20060102") {
				continue
			}
		}
		file, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 4096), maxLineBytes)
		for scanner.Scan() {
			var record Record
			if json.Unmarshal(scanner.Bytes(), &record) != nil {
				continue
			}
			if !matches(record, filter) {
				continue
			}
			out = append(out, record)
		}
		file.Close()
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[len(out)-filter.Limit:]
	}
	return out, nil
}

func matches(record Record, filter Filter) bool {
	if filter.Principal != "" && record.Principal != filter.Principal {
		return false
	}
	if filter.Reason != "" && record.Reason != filter.Reason {
		return false
	}
	if !filter.Since.IsZero() {
		at, err := time.Parse("2006-01-02T15:04:05.000Z", record.TS)
		if err != nil || at.Before(filter.Since) {
			return false
		}
	}
	return true
}
