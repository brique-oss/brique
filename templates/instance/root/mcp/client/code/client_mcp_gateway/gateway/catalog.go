// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// catalog.go
//
// What a server offers, and what a model is shown of it.
//
// A catalog is the bounded listing of a server's tools, kept only for the tools the registry offers,
// each with a fingerprint of its name, description and input schema. It is cached per server entry and
// listed again after catalogTTL, so a call checks the tool against a recent listing: a tool whose
// fingerprint changed since a projection is refused as stale, never remapped.
//
// A projection is what one inference is given: the model-visible name of each tool (provider-safe,
// unique, never parsed back) bound to its server, remote name, fingerprint and schema. It is written to
// the private store before it is returned, so a pending call survives a gateway restart, and it expires.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxCatalogPages     = 5
	maxCatalogTools     = 200
	maxCatalogBytes     = 512 << 10
	maxToolSchemaBytes  = 64 << 10
	maxToolDescription  = 2048
	maxProjectionTools  = 40
	maxModelNameLength  = 64
	catalogTTL          = 5 * time.Minute
	catalogDeadline     = 20 * time.Second
	projectionLife      = 24 * time.Hour
	projectionsDir      = "projections"
	modelNameHashLength = 8
)

var (
	errCatalogTooLarge = errors.New("catalog exceeds the gateway's limits")
	modelNameUnsafe    = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	projectionIDRule   = regexp.MustCompile(`^p_[0-9a-f]{32}$`)
)

// catalogTool is one remote tool as listed.
type catalogTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Fingerprint string          `json:"fingerprint"`

	resolveOnce sync.Once
	resolved    *jsonschema.Resolved
	resolveErr  error
}

// validator compiles the input schema once; a schema that cannot be compiled refuses every call.
func (t *catalogTool) validator() (*jsonschema.Resolved, error) {
	t.resolveOnce.Do(func() {
		var schema jsonschema.Schema
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			t.resolveErr = fmt.Errorf("input schema cannot be read: %w", err)
			return
		}
		t.resolved, t.resolveErr = schema.Resolve(nil) // no loader: a remote $ref is never fetched
	})
	return t.resolved, t.resolveErr
}

type catalog struct {
	serverKey string
	listedAt  time.Time
	tools     map[string]*catalogTool
}

// fingerprint binds a tool's name, description and schema.
func fingerprint(name, description string, schema json.RawMessage) string {
	raw, _ := json.Marshal([]any{name, description, schema})
	return sha256Hex(raw)[:32]
}

// listCatalog lists a server's tools within the gateway's bounds.
func listCatalog(ctx context.Context, session *mcp.ClientSession, server *Server, now time.Time) (*catalog, error) {
	out := &catalog{serverKey: server.key, listedAt: now, tools: map[string]*catalogTool{}}
	cursor, total, bytes := "", 0, 0
	for page := 0; ; page++ {
		if page == maxCatalogPages {
			return nil, errCatalogTooLarge
		}
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, tool := range result.Tools {
			total++
			if total > maxCatalogTools {
				return nil, errCatalogTooLarge
			}
			schema, _ := json.Marshal(tool.InputSchema)
			bytes += len(tool.Name) + len(tool.Description) + len(schema)
			if bytes > maxCatalogBytes {
				return nil, errCatalogTooLarge
			}
			if !server.offers(tool.Name) || len(schema) > maxToolSchemaBytes || tool.InputSchema == nil {
				continue
			}
			description := tool.Description
			if len(description) > maxToolDescription {
				description = truncateUTF8(description, maxToolDescription)
			}
			out.tools[tool.Name] = &catalogTool{Name: tool.Name, Description: description, InputSchema: schema,
				Fingerprint: fingerprint(tool.Name, description, schema)}
		}
		if result.NextCursor == "" {
			return out, nil
		}
		cursor = result.NextCursor
	}
}

// catalogCache holds the last listing of each server entry; one listing at a time per server.
type catalogCache struct {
	mu       sync.Mutex
	catalogs map[string]*catalog
	listing  map[string]*sync.Mutex
}

func newCatalogCache() *catalogCache {
	return &catalogCache{catalogs: map[string]*catalog{}, listing: map[string]*sync.Mutex{}}
}

func (c *catalogCache) lock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.listing[key]
	if !ok {
		l = &sync.Mutex{}
		c.listing[key] = l
	}
	return l
}

func (c *catalogCache) cached(key string) *catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.catalogs[key]
}

// get returns a listing of the server no older than maxAge, listing it again otherwise.
func (c *catalogCache) get(ctx context.Context, maxAge time.Duration, now time.Time, list func(context.Context) (*catalog, error), server *Server) (*catalog, error) {
	if cat := c.cached(server.key); cat != nil && now.Sub(cat.listedAt) <= maxAge {
		return cat, nil
	}
	l := c.lock(server.key)
	l.Lock()
	defer l.Unlock()
	if cat := c.cached(server.key); cat != nil && now.Sub(cat.listedAt) <= maxAge {
		return cat, nil // listed by a concurrent caller meanwhile
	}
	listCtx, cancel := context.WithTimeout(ctx, catalogDeadline)
	defer cancel()
	cat, err := list(listCtx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.catalogs[server.key] = cat
	c.mu.Unlock()
	return cat, nil
}

// ProjectedTool binds one model-visible name.
type ProjectedTool struct {
	ModelName   string          `json:"model_name"`
	Server      string          `json:"server"`
	Tool        string          `json:"tool"`
	Fingerprint string          `json:"fingerprint"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Projection is what one inference is given.
type Projection struct {
	ID              string          `json:"projection_id"`
	RegistryVersion int64           `json:"registry_version"`
	CreatedAt       time.Time       `json:"created_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Tools           []ProjectedTool `json:"tools"`
}

func (p *Projection) lookup(modelName string) (ProjectedTool, bool) {
	for _, tool := range p.Tools {
		if tool.ModelName == modelName {
			return tool, true
		}
	}
	return ProjectedTool{}, false
}

// modelNames gives each (server, tool) a provider-safe name, unique in the projection. A name that had to
// be changed or shortened carries a hash of the original pair, so two different pairs never meet.
func modelNames(pairs [][2]string) []string {
	names := make([]string, len(pairs))
	used := map[string]bool{}
	for i, pair := range pairs {
		original := pair[0] + "__" + pair[1]
		name := modelNameUnsafe.ReplaceAllString(original, "_")
		if name != original || len(name) > maxModelNameLength || used[name] {
			hash := sha256Hex([]byte(original))[:modelNameHashLength]
			if len(name) > maxModelNameLength-modelNameHashLength-1 {
				name = name[:maxModelNameLength-modelNameHashLength-1]
			}
			name = name + "_" + hash
		}
		used[name] = true
		names[i] = name
	}
	return names
}

// projectionStore keeps projections in the private store.
type projectionStore struct {
	dir string
	mu  sync.Mutex
}

func newProjectionStore(storeDir string) (*projectionStore, error) {
	dir := filepath.Join(storeDir, projectionsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("projections: %w", err)
	}
	return &projectionStore{dir: dir}, nil
}

func newProjectionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "p_" + hex.EncodeToString(b[:])
}

// save writes a projection durably (temporary file, flush, rename, directory flush).
func (s *projectionStore) save(p *Projection) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, p.ID+".json")
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// load reads a projection that is still valid.
func (s *projectionStore) load(id string, now time.Time) (*Projection, error) {
	if !projectionIDRule.MatchString(id) {
		return nil, errors.New("projection_id is malformed")
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return nil, errors.New("projection unknown")
	}
	var p Projection
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("projection unreadable")
	}
	if !now.Before(p.ExpiresAt) {
		return nil, errors.New("projection expired")
	}
	return &p, nil
}

// prune removes expired projections.
func (s *projectionStore) prune(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			if strings.HasSuffix(name, ".tmp") {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > projectionLife {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// sortedToolNames returns the tools of a catalog in order.
func (c *catalog) sortedToolNames() []string {
	out := make([]string, 0, len(c.tools))
	for name := range c.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// truncateUTF8 cuts a string to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}
