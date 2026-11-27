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

// tokens.go
//
// The bearer tokens of the remote MCP profile. The list lives in the wrapper's private store (tokens.json, owner-only,
// outside the instance) and holds a label, the SHA-256 of each token and an optional expiry: never a token itself. It is
// read again when the file changes, so a revocation takes effect on the next request. A token is compared in constant
// time; its label is the user of the session it opens (the SDK refuses a session used with another token) and the key of
// its rate. The operator manages the list with the server binary itself:
//
//	mcp_server_go token add <label> [--expires 720h] --store <dir>   prints the token once
//	mcp_server_go token revoke <label> --store <dir>
//	mcp_server_go token list --store <dir>

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

const (
	tokensFile      = "tokens.json"
	tokensFormat    = "brique-mcp-tokens"
	tokenPrefix     = "bmcp_"
	maxTokens       = 256
	defaultRate     = 10.0
	maxTokensFile   = 1 << 20
	maxBearerLength = 512
)

var tokenLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type tokenEntry struct {
	Label     string     `json:"label"`
	SHA256    string     `json:"sha256"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type tokenFile struct {
	Format string       `json:"format"`
	Tokens []tokenEntry `json:"tokens"`
}

// tokenStore reads the list again when the file changes; a list that is missing or unreadable accepts no token.
type tokenStore struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	seen    os.FileInfo
	entries []tokenEntry
}

func newTokenStore(storeDir string) *tokenStore {
	return &tokenStore{path: filepath.Join(storeDir, tokensFile), now: time.Now}
}

func (s *tokenStore) current() []tokenEntry {
	info, err := os.Stat(s.path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.seen, s.entries = nil, nil
		return nil
	}
	if s.seen != nil && os.SameFile(s.seen, info) && info.Size() == s.seen.Size() && info.ModTime().Equal(s.seen.ModTime()) {
		return s.entries
	}
	file, err := readTokenFile(s.path)
	s.seen = info
	if err != nil {
		s.entries = nil
		return nil
	}
	s.entries = file.Tokens
	return s.entries
}

func readTokenFile(path string) (*tokenFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxTokensFile {
		return nil, errors.New("token list too large")
	}
	var file tokenFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Format != tokensFormat {
		return nil, errors.New("token list unreadable")
	}
	return &file, nil
}

func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return []byte(hex.EncodeToString(sum[:]))
}

// verify is the SDK's TokenVerifier: the label of the matching, unexpired token, or ErrInvalidToken.
func (s *tokenStore) verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if len(token) == 0 || len(token) > maxBearerLength {
		return nil, auth.ErrInvalidToken
	}
	presented := digest(token)
	var found *tokenEntry
	for _, entry := range s.current() { // every entry is compared: the time does not tell which one matched
		if subtle.ConstantTimeCompare(presented, []byte(entry.SHA256)) == 1 {
			match := entry
			found = &match
		}
	}
	if found == nil || (found.ExpiresAt != nil && !s.now().Before(*found.ExpiresAt)) {
		return nil, auth.ErrInvalidToken
	}
	info := &auth.TokenInfo{UserID: found.Label}
	if found.ExpiresAt != nil {
		info.Expiration = *found.ExpiresAt
	}
	return info, nil
}

// tokenRate is a token bucket per label: a client that floods is refused with 429, the others go on.
type tokenRate struct {
	rate float64
	mu   sync.Mutex
	b    map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newTokenRate(rate float64) *tokenRate {
	if rate <= 0 {
		rate = defaultRate
	}
	return &tokenRate{rate: rate, b: map[string]*bucket{}}
}

func (t *tokenRate) allow(label string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	burst := 2 * t.rate
	b, ok := t.b[label]
	if !ok {
		b = &bucket{tokens: burst, at: now}
		t.b[label] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.at).Seconds()*t.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (t *tokenRate) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.TokenInfoFromContext(r.Context())
		if info != nil && !t.allow(info.UserID, time.Now()) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests for this token", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- the operator's commands --------------------------------------------------------------------------------------

// runTokenCommand handles `token add|revoke|list`; it never starts the wrapper.
func runTokenCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: token add <label> [--expires <duration>] --store <dir> | token revoke <label> --store <dir> | token list --store <dir>")
	}
	verb, rest := args[0], args[1:]
	flags := flag.NewFlagSet("token "+verb, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	store := flags.String("store", os.Getenv("BRIQUE_WRAPPER_STORE"), "the private store of the remote MCP wrapper")
	expires := flags.Duration("expires", 0, "lifetime of the token (none by default)")
	label := ""
	if verb != "list" {
		if len(rest) == 0 {
			return errors.New("a label is required")
		}
		label, rest = rest[0], rest[1:]
		if !tokenLabel.MatchString(label) {
			return errors.New("a label is [a-z0-9][a-z0-9._-]{0,63}")
		}
	}
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if *store == "" {
		return errors.New("--store is required (the private directory of the wrapper mcp_remote)")
	}
	path := filepath.Join(*store, tokensFile)
	file, err := readTokenFile(path)
	if errors.Is(err, os.ErrNotExist) {
		file, err = &tokenFile{Format: tokensFormat}, nil
	}
	if err != nil {
		return err
	}
	switch verb {
	case "add":
		for _, e := range file.Tokens {
			if e.Label == label {
				return fmt.Errorf("label %q exists: revoke it first", label)
			}
		}
		if len(file.Tokens) >= maxTokens {
			return fmt.Errorf("at most %d tokens", maxTokens)
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		token := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
		entry := tokenEntry{Label: label, SHA256: string(digest(token)), CreatedAt: time.Now().UTC()}
		if *expires > 0 {
			at := entry.CreatedAt.Add(*expires)
			entry.ExpiresAt = &at
		}
		file.Tokens = append(file.Tokens, entry)
		if err := writeTokenFile(*store, path, file); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\n", token)
		return nil
	case "revoke":
		kept := file.Tokens[:0]
		for _, e := range file.Tokens {
			if e.Label != label {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(file.Tokens) {
			return fmt.Errorf("no token labelled %q", label)
		}
		file.Tokens = kept
		return writeTokenFile(*store, path, file)
	case "list":
		sort.Slice(file.Tokens, func(i, j int) bool { return file.Tokens[i].Label < file.Tokens[j].Label })
		for _, e := range file.Tokens {
			expiry := "never"
			if e.ExpiresAt != nil {
				expiry = e.ExpiresAt.Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "%s\tcreated %s\texpires %s\n", e.Label, e.CreatedAt.Format(time.RFC3339), expiry)
		}
		return nil
	}
	return fmt.Errorf("unknown token command %q", verb)
}

// writeTokenFile replaces the list atomically, owner-only.
func writeTokenFile(dir, path string, file *tokenFile) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(file, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
