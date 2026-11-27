// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package gateway

// registry.go
//
// The MCP server registry: which external servers the instance may reach, how, and which tools of each.
// The operator installs it on the machine (brique-sec wrapper-file install client_mcp_gateway
// registry.json); the gateway reads that copy in its private store, never a file of the instance. It is
// read again when the file changes, and a registry that is missing or invalid refuses everything: a
// revocation takes effect on the next call, and a mistake fails closed.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// RegistryFile is the name of the registry in the private store.
const RegistryFile = "registry.json"

const registryFormat = "brique-mcp-registry"

// Limits of the registry, and the defaults of a server that sets none.
const (
	maxRegistryBytes         = 1 << 20
	maxServers               = 64
	defaultConcurrency       = 8
	maxConcurrency           = 64
	defaultCallDeadline      = 60 * time.Second
	maxCallDeadline          = 300 * time.Second
	defaultResultInlineBytes = 256 << 10
	maxResultInlineBytes     = 256 << 10
	defaultResultMaxBytes    = 4 << 20
	maxResultMaxBytes        = 4 << 20
)

var (
	aliasPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	secretNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	headerNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
	toolNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_.\-/]{1,128}$`)

	errNoRegistry = errors.New("no MCP registry is installed")
)

// Registry is the parsed, validated registry.
type Registry struct {
	Format             string             `json:"format"`
	Version            int64              `json:"version"`
	AuthorityPublicKey string             `json:"authority_public_key,omitempty"`
	Plans              []string           `json:"plans,omitempty"`
	Servers            map[string]*Server `json:"servers"`

	publicKey ed25519.PublicKey
	plans     map[string]bool
}

// Server is one registered MCP server.
type Server struct {
	Description         string                `json:"description,omitempty"`
	Enabled             *bool                 `json:"enabled,omitempty"`
	Transport           string                `json:"transport"`
	Endpoint            string                `json:"endpoint"`
	AllowPrivateNetwork bool                  `json:"allow_private_network,omitempty"`
	Auth                Auth                  `json:"auth"`
	TrustDomain         string                `json:"trust_domain,omitempty"`
	MaxDataClass        string                `json:"max_data_class,omitempty"`
	Limits              Limits                `json:"limits,omitempty"`
	Catalog             CatalogPolicy         `json:"catalog"`
	Tools               map[string]ToolPolicy `json:"tools,omitempty"`

	alias    string
	endpoint *url.URL
	allowed  map[string]bool // explicit catalog: the tools offered; nil means every tool
	key      string          // identity of this entry: a change of any field is a new session and a new catalog
}

// Auth selects the credential the gateway sends to the server. Secrets name machine secrets granted to
// this wrapper (run.secrets), delivered in MCP_SECRET_<NAME> (the engine reserves BRIQUE_* to itself).
type Auth struct {
	Mode    string            `json:"mode"`
	Header  string            `json:"header,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
	// oauth: the authorization server the grant must come from, the client registered with it beforehand, the most
	// scopes the grant may hold (the ceiling of a login and of every grant used), and the loopback port of the login
	// callback when the client is registered with a fixed redirect (0: any free port).
	Issuer       string   `json:"issuer,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	RedirectPort int      `json:"redirect_port,omitempty"`
}

// Limits of one server.
type Limits struct {
	Concurrency       int `json:"concurrency,omitempty"`
	CallDeadlineS     int `json:"call_deadline_s,omitempty"`
	ResultInlineBytes int `json:"result_inline_bytes,omitempty"`
	ResultMaxBytes    int `json:"result_max_bytes,omitempty"`
}

// CatalogPolicy says which remote tools are offered: "explicit" (the listed names only) or "all".
type CatalogPolicy struct {
	Mode  string   `json:"mode"`
	Tools []string `json:"tools,omitempty"`
}

// ToolPolicy is the trusted configuration of one remote tool.
type ToolPolicy struct {
	Class     string        `json:"class,omitempty"`
	Retry     string        `json:"retry,omitempty"`
	Arguments ArgumentsRule `json:"arguments,omitempty"`
}

// ArgumentsRule lists the top-level argument fields that may never be sent to the tool.
type ArgumentsRule struct {
	Deny []string `json:"deny,omitempty"`
}

// Enabled reports whether the server may be used.
func (s *Server) enabled() bool { return s.Enabled == nil || *s.Enabled }

// offers reports whether the registry lets the tool of this server be offered and called.
func (s *Server) offers(tool string) bool { return s.allowed == nil || s.allowed[tool] }

func (s *Server) concurrency() int {
	if s.Limits.Concurrency <= 0 {
		return defaultConcurrency
	}
	return s.Limits.Concurrency
}

func (s *Server) callDeadline() time.Duration {
	if s.Limits.CallDeadlineS <= 0 {
		return defaultCallDeadline
	}
	return time.Duration(s.Limits.CallDeadlineS) * time.Second
}

func (s *Server) resultInlineBytes() int {
	if s.Limits.ResultInlineBytes <= 0 {
		return defaultResultInlineBytes
	}
	return s.Limits.ResultInlineBytes
}

func (s *Server) resultMaxBytes() int {
	if s.Limits.ResultMaxBytes <= 0 {
		return defaultResultMaxBytes
	}
	return s.Limits.ResultMaxBytes
}

// deniedArgument returns the first top-level argument the registry forbids for the tool.
func (s *Server) deniedArgument(tool string, arguments map[string]any) (string, bool) {
	for _, field := range s.Tools[tool].Arguments.Deny {
		if _, present := arguments[field]; present {
			return field, true
		}
	}
	return "", false
}

// missingSecret is the first secret of the server's credential that the gateway did not receive, or "".
func (s *Server) missingSecret(secrets map[string]string) string {
	for _, name := range s.Auth.Secrets {
		if secrets[name] == "" {
			return name
		}
	}
	return ""
}

// secretVariable is the environment variable that carries a machine secret granted to this wrapper.
func secretVariable(name string) string { return "MCP_SECRET_" + strings.ToUpper(name) }

// planAllowed reports whether the plan at that origin (its context and capacity) is registered.
func (r *Registry) planAllowed(context, capacity string) bool {
	return r.plans[planReference(context, capacity)]
}

func planReference(context, capacity string) string {
	return strings.TrimSuffix(context, "/") + "/capacity/" + capacity
}

// parseRegistry decodes and validates a registry. Unknown fields are refused: a typo must not be read as
// an absent restriction.
func parseRegistry(raw []byte) (*Registry, error) {
	if len(raw) > maxRegistryBytes {
		return nil, fmt.Errorf("registry is larger than %d bytes", maxRegistryBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var r Registry
	if err := decoder.Decode(&r); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("registry: trailing data")
	}
	if r.Format != registryFormat {
		return nil, fmt.Errorf("registry: format must be %q", registryFormat)
	}
	if r.Version < 1 {
		return nil, errors.New("registry: version must be a positive integer")
	}
	if r.AuthorityPublicKey != "" {
		key, err := base64.RawURLEncoding.DecodeString(r.AuthorityPublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, errors.New("registry: authority_public_key must be a base64url Ed25519 public key")
		}
		r.publicKey = ed25519.PublicKey(key)
	}
	r.plans = map[string]bool{}
	for _, plan := range r.Plans {
		context, capacity, ok := strings.Cut(plan, "/capacity/")
		if !ok || !strings.HasPrefix(context, "/root") || capacity == "" || strings.Contains(capacity, "/") {
			return nil, fmt.Errorf("registry: plan %q must be written /root/<context>/capacity/<name>", plan)
		}
		r.plans[plan] = true
	}
	if len(r.Servers) > maxServers {
		return nil, fmt.Errorf("registry: more than %d servers", maxServers)
	}
	for alias, server := range r.Servers {
		if server == nil {
			return nil, fmt.Errorf("registry: server %q is empty", alias)
		}
		if err := validateServer(alias, server); err != nil {
			return nil, fmt.Errorf("registry: server %q: %w", alias, err)
		}
	}
	return &r, nil
}

func validateServer(alias string, s *Server) error {
	if !aliasPattern.MatchString(alias) {
		return errors.New("alias must match [a-z][a-z0-9_-]{0,31}")
	}
	s.alias = alias
	if s.Transport != "streamable_http" {
		return errors.New(`transport must be "streamable_http"`)
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("endpoint must be an absolute URL with a host and no user information")
	}
	switch {
	case endpoint.Scheme == "https":
	case endpoint.Scheme == "http" && s.AllowPrivateNetwork:
	default:
		return errors.New("endpoint must use https (http only with allow_private_network)")
	}
	s.endpoint = endpoint
	switch s.Auth.Mode {
	case "none":
		if len(s.Auth.Secrets) != 0 || s.Auth.Header != "" {
			return errors.New("auth none takes no secret and no header")
		}
	case "static":
		if len(s.Auth.Secrets) != 1 || s.Auth.Secrets["token"] == "" {
			return errors.New("auth static takes exactly one secret, token")
		}
		if s.Auth.Header != "" && !headerNamePattern.MatchString(s.Auth.Header) {
			return errors.New("auth header is not a header name")
		}
	case "basic":
		if len(s.Auth.Secrets) != 2 || s.Auth.Secrets["username"] == "" || s.Auth.Secrets["password"] == "" {
			return errors.New("auth basic takes exactly two secrets, username and password")
		}
		if s.Auth.Header != "" {
			return errors.New("auth basic takes no header")
		}
	case "oauth":
		issuer, err := url.Parse(s.Auth.Issuer)
		if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" ||
			!(issuer.Scheme == "https" || (issuer.Scheme == "http" && s.AllowPrivateNetwork)) {
			return errors.New("auth oauth needs issuer, an https URL (http only with allow_private_network)")
		}
		if s.Auth.ClientID == "" || len(s.Auth.ClientID) > 256 {
			return errors.New("auth oauth needs client_id, the client registered with the issuer")
		}
		for name := range s.Auth.Secrets {
			if name != "client_secret" {
				return errors.New("auth oauth takes at most one secret, client_secret")
			}
		}
		if s.Auth.Header != "" {
			return errors.New("auth oauth takes no header")
		}
		if s.Auth.RedirectPort != 0 && (s.Auth.RedirectPort < 1024 || s.Auth.RedirectPort > 65535) {
			return errors.New("auth redirect_port must be between 1024 and 65535")
		}
		for _, scope := range s.Auth.Scopes {
			if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \"") {
				return fmt.Errorf("auth scope %q is not a scope", scope)
			}
		}
	default:
		return errors.New("auth mode must be none, static, basic or oauth")
	}
	if s.Auth.Mode != "oauth" && (s.Auth.Issuer != "" || s.Auth.ClientID != "" || len(s.Auth.Scopes) != 0 || s.Auth.RedirectPort != 0) {
		return errors.New("issuer, client_id, scopes and redirect_port belong to auth oauth")
	}
	for _, name := range s.Auth.Secrets {
		if !secretNamePattern.MatchString(name) {
			return fmt.Errorf("secret name %q must match [a-z][a-z0-9_]{0,63}", name)
		}
	}
	l := s.Limits
	if l.Concurrency < 0 || l.Concurrency > maxConcurrency {
		return fmt.Errorf("limits.concurrency must be between 1 and %d", maxConcurrency)
	}
	if l.CallDeadlineS < 0 || time.Duration(l.CallDeadlineS)*time.Second > maxCallDeadline {
		return fmt.Errorf("limits.call_deadline_s must be between 1 and %d", int(maxCallDeadline/time.Second))
	}
	if l.ResultInlineBytes < 0 || l.ResultInlineBytes > maxResultInlineBytes {
		return fmt.Errorf("limits.result_inline_bytes must be between 1 and %d", maxResultInlineBytes)
	}
	if l.ResultMaxBytes < 0 || l.ResultMaxBytes > maxResultMaxBytes {
		return fmt.Errorf("limits.result_max_bytes must be between 1 and %d", maxResultMaxBytes)
	}
	switch s.Catalog.Mode {
	case "explicit":
		if len(s.Catalog.Tools) == 0 {
			return errors.New("an explicit catalog lists at least one tool")
		}
		s.allowed = map[string]bool{}
		for _, tool := range s.Catalog.Tools {
			if !toolNamePattern.MatchString(tool) {
				return fmt.Errorf("catalog tool %q is not a tool name", tool)
			}
			s.allowed[tool] = true
		}
	case "all":
		if len(s.Catalog.Tools) != 0 {
			return errors.New("catalog all lists no tool")
		}
	default:
		return errors.New(`catalog.mode must be "explicit" or "all"`)
	}
	for tool, policy := range s.Tools {
		if !toolNamePattern.MatchString(tool) {
			return fmt.Errorf("tool %q is not a tool name", tool)
		}
		switch policy.Class {
		case "", "read", "write":
		default:
			return fmt.Errorf(`tool %q: class must be "read" or "write"`, tool)
		}
		switch policy.Retry {
		case "", "never":
		default:
			return fmt.Errorf(`tool %q: retry must be "never"`, tool)
		}
	}
	entry, _ := json.Marshal(s)
	sum := sha256.Sum256(append([]byte(alias+"\x00"), entry...))
	s.key = hex.EncodeToString(sum[:16])
	return nil
}

// sortedAliases returns the enabled servers' aliases in order.
func (r *Registry) sortedAliases() []string {
	out := make([]string, 0, len(r.Servers))
	for alias, server := range r.Servers {
		if server.enabled() {
			out = append(out, alias)
		}
	}
	sort.Strings(out)
	return out
}

// registryStore reads the registry file again when it changes (another file, size or modification time), so the
// operator's install takes effect without a restart.
type registryStore struct {
	path string

	mu      sync.Mutex
	seen    os.FileInfo // the file the current answer was read from
	current *Registry
	err     error
}

func newRegistryStore(storeDir string) *registryStore {
	return &registryStore{path: storeDir + string(os.PathSeparator) + RegistryFile}
}

// get returns the registry in force, or the reason there is none. Never a stale registry after a change.
func (s *registryStore) get() (*Registry, error) {
	info, statErr := os.Stat(s.path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if statErr != nil {
		s.current, s.err, s.seen = nil, errNoRegistry, nil
		return nil, s.err
	}
	if s.seen != nil && os.SameFile(s.seen, info) && info.Size() == s.seen.Size() && info.ModTime().Equal(s.seen.ModTime()) {
		return s.current, s.err
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.current, s.err = nil, fmt.Errorf("registry cannot be read: %w", err)
	} else {
		s.current, s.err = parseRegistry(raw)
	}
	s.seen = info
	return s.current, s.err
}

// CheckRegistryFile validates a registry file exactly as the gateway will read it, before the operator installs it, and
// returns a one-line summary per server.
func CheckRegistryFile(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := parseRegistry(raw)
	if err != nil {
		return nil, err
	}
	lines := []string{fmt.Sprintf("registry version %d, %d server(s), %d plan(s), authority key %v", r.Version, len(r.Servers), len(r.Plans), r.publicKey != nil)}
	for _, alias := range sortedKeys(r.Servers) {
		s := r.Servers[alias]
		tools := "all tools"
		if s.allowed != nil {
			tools = strings.Join(s.Catalog.Tools, ", ")
		}
		lines = append(lines, fmt.Sprintf("  %s: %s, auth %s, enabled %v, %s", alias, s.Endpoint, s.Auth.Mode, s.enabled(), tools))
	}
	return lines, nil
}

func sortedKeys(m map[string]*Server) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
