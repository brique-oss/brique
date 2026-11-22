/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"brique_engine/platform/diagnostic"
)

//go:embed web_ui_static
var webUIStatic embed.FS

const (
	webUIMaxBody    = 16 << 10
	webUILogTail    = 64 << 10
	webUILogMaxRows = 400
)

// WebUIOptions configures the local administration page of the launcher.
type WebUIOptions struct {
	Store                   Store
	DataDirectory           string
	ControlRuntimeDirectory string
	// Address is the loopback address to listen on; "127.0.0.1:0" when empty.
	Address string
	// Creation says whether instances can be created from the page (zero value: not available).
	Creation CreationDefaults
	// Publish says whether instances can publish their web UIs from this launcher.
	Publish PublishTools
	// OpenBrowser opens an address in a browser; tests replace it, nil means the real one.
	OpenBrowser func(browser, url string) error
	// DiagnosisTLS replaces the system trust of the diagnosis; tests only.
	DiagnosisTLS *tls.Config
	// Token is the durable access token; a fresh random one is used when empty.
	Token string
}

// WebUI serves the local administration page. It is a client of the launcher control
// protocol and of the registry: it holds no process and no Brique key. Every API call
// needs the ephemeral token (sent as a header, so a foreign page cannot forge it) and
// must come from the exact loopback authority that was bound.
type WebUI struct {
	options  WebUIOptions
	token    string
	listener net.Listener
	server   *http.Server
	hosts    map[string]bool
	// engineMu serialises the calls to an engine: its launcher route holds one connection and a second one would cut the first.
	engineMu sync.Mutex
	// diagnosing allows one connection diagnosis at a time.
	diagnosing sync.Mutex
	publisher  *publisher
}

// StartWebUI binds a loopback listener and starts serving.
func StartWebUI(options WebUIOptions) (*WebUI, error) {
	if options.DataDirectory == "" {
		return nil, errors.New("launcher data directory is required")
	}
	address := options.Address
	if address == "" {
		address = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("the launcher page only listens on a loopback IP address")
	}
	token := options.Token
	if token == "" {
		var err error
		if token, err = newWebUIToken(); err != nil {
			return nil, err
		}
	} else if len(token) < 32 {
		return nil, errors.New("the page token is too short")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	ui := &WebUI{options: options, token: token, listener: listener, publisher: newPublisher()}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	ui.hosts = map[string]bool{net.JoinHostPort(host, port): true}
	if host == "127.0.0.1" {
		ui.hosts[net.JoinHostPort("localhost", port)] = true
	}
	static, err := fs.Sub(webUIStatic, "web_ui_static")
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/instances", ui.api(ui.listInstances))
	mux.HandleFunc("POST /api/instances", ui.api(ui.addInstance))
	mux.HandleFunc("GET /api/browser", ui.api(ui.browserSection))
	mux.HandleFunc("POST /api/browser/associate", ui.api(ui.associate))
	mux.HandleFunc("POST /api/browser/dissociate", ui.api(ui.dissociate))
	mux.HandleFunc("POST /api/browser/open", ui.api(ui.openBrowser))
	mux.HandleFunc("POST /api/create-instance", ui.api(ui.createInstance))
	mux.HandleFunc("POST /api/instances/{alias}/{action}", ui.api(ui.control))
	mux.HandleFunc("POST /api/instances/{alias}/use-runtime", ui.api(ui.useRuntimeHandler))
	mux.HandleFunc("POST /api/instances/{alias}/publish", ui.api(ui.publish))
	mux.HandleFunc("POST /api/instances/{alias}/autostart", ui.api(ui.setAutostart))
	mux.HandleFunc("GET /api/instances/{alias}/logs", ui.api(ui.logs))
	mux.HandleFunc("GET /api/instances/{alias}/mcp", ui.api(ui.mcpStates))
	mux.HandleFunc("POST /api/instances/{alias}/mcp", ui.api(ui.mcpSet))
	mux.HandleFunc("POST /api/diagnose", ui.api(ui.diagnose))
	mux.HandleFunc("POST /api/shutdown", ui.api(ui.shutdown))
	ui.server = &http.Server{Handler: ui.guard(mux), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = ui.server.Serve(listener) }()
	return ui, nil
}

func newWebUIToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

const (
	// DefaultWebUIAddress is where `serve` publishes the page, so that it can be bookmarked.
	DefaultWebUIAddress = "127.0.0.1:18300"
	webUITokenFile      = "ui/token"
	webUIAddressFile    = "ui/address"
)

// LoadOrCreateWebUIToken returns the durable page token kept in the private data directory
// (mode 0600). The page keeps it in the browser once opened with the token in the URL, so a
// plain bookmark works afterwards. It is the one place the token lives besides the browser.
func LoadOrCreateWebUIToken(dataDirectory string) (string, error) {
	path := filepath.Join(dataDirectory, webUITokenFile)
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); len(token) >= 32 {
			return token, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	token, err := newWebUIToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", err
	}
	return token, nil
}

// PublishWebUIAddress records where the page listens, for `brique-launcher ui`.
func PublishWebUIAddress(dataDirectory, address string) error {
	path := filepath.Join(dataDirectory, webUIAddressFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(address+"\n"), 0o600)
}

// RetractWebUIAddress removes the recorded address when the page stops.
func RetractWebUIAddress(dataDirectory string) {
	_ = os.Remove(filepath.Join(dataDirectory, webUIAddressFile))
}

// ReadWebUIAddress returns the recorded page address and the durable token, or an error when
// no supervisor ever published a page.
func ReadWebUIAddress(dataDirectory string) (string, string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDirectory, webUIAddressFile))
	if err != nil {
		return "", "", err
	}
	token, err := LoadOrCreateWebUIToken(dataDirectory)
	return strings.TrimSpace(string(raw)), token, err
}

// Address is the address the page listens on.
func (ui *WebUI) Address() string { return ui.listener.Addr().String() }

// URL is the address to open. The token sits in the fragment: it never reaches a server
// log or a Referer, and the page reads it and sends it back as a header.
func (ui *WebUI) URL() string {
	return "http://" + ui.listener.Addr().String() + "/#token=" + ui.token
}

func (ui *WebUI) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return ui.server.Shutdown(ctx)
}

// guard refuses a request whose Host is not the bound loopback authority (DNS rebinding)
// or whose Origin, when present, is another site.
func (ui *WebUI) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ui.hosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

type webUIHandler func(r *http.Request) (any, int, error)

// api wraps an API handler: token, JSON content type on writes, bounded body, JSON reply.
func (ui *WebUI) api(handler webUIHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(ui.token)) != 1 {
			writeWebUIJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if r.Method == http.MethodPost {
			if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(mediaType) != "application/json" {
				writeWebUIJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "application/json required"})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, webUIMaxBody)
		}
		value, status, err := handler(r)
		if err != nil {
			if status == 0 {
				status = http.StatusBadGateway
			}
			writeWebUIJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeWebUIJSON(w, http.StatusOK, value)
	}
}

func writeWebUIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeWebUIBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid request body")
	}
	return nil
}

// WebUIInstance is the page's view of one registered instance.
type WebUIInstance struct {
	Alias       string `json:"alias"`
	RootPath    string `json:"root_path"`
	Supervision string `json:"supervision"`
	Autostart   bool   `json:"autostart"`
	State       State  `json:"state"`
	// Owned is false for an engine that was already running: the launcher shows it but does not stop it.
	Owned  bool   `json:"owned"`
	WebURL string `json:"web_url,omitempty"`
	Error  string `json:"error,omitempty"`
	// Publication is none, current, obsolete (the interfaces changed since) or unknown.
	Publication string        `json:"publication"`
	Publishing  PublishStatus `json:"publishing"`
	// ChangedInterfaces names the interfaces edited, added or removed since the publication.
	ChangedInterfaces []string `json:"changed_interfaces,omitempty"`
}

type webUIInstances struct {
	Create       CreationDefaults `json:"create"`
	Publish      PublishTools     `json:"publish_tools"`
	SupervisorUp bool             `json:"supervisor_up"`
	Instances    []WebUIInstance  `json:"instances"`
}

func (ui *WebUI) listInstances(_ *http.Request) (any, int, error) {
	registry, err := ui.options.Store.List()
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	result := webUIInstances{Instances: []WebUIInstance{}, Create: ui.options.Creation, Publish: ui.options.Publish}
	statuses := map[string]Status{}
	if response, err := ui.call(LauncherControlRequest{Operation: "list"}); err == nil {
		result.SupervisorUp = true
		for _, status := range response.Statuses {
			statuses[status.Alias] = status
		}
	}
	for _, instance := range registry.Instances {
		item := WebUIInstance{Alias: instance.Alias, RootPath: instance.RootPath, Supervision: instance.Supervision,
			Autostart: instance.Autostart, State: StateStopped, Publishing: ui.publisher.status(instance.Alias)}
		item.Publication, item.ChangedInterfaces = ui.publicationState(instance)
		if status, ok := statuses[instance.Alias]; ok {
			item.State, item.Owned, item.WebURL, item.Error = status.State, status.Owned, status.WebURL, status.Error
		}
		result.Instances = append(result.Instances, item)
	}
	sort.Slice(result.Instances, func(i, j int) bool { return result.Instances[i].Alias < result.Instances[j].Alias })
	return result, 0, nil
}

func (ui *WebUI) call(request LauncherControlRequest) (LauncherControlResponse, error) {
	return CallLauncherControl(ui.options.DataDirectory, ui.options.ControlRuntimeDirectory, request)
}

func (ui *WebUI) control(r *http.Request) (any, int, error) {
	alias, action := r.PathValue("alias"), r.PathValue("action")
	if !validAlias(alias) {
		return nil, http.StatusBadRequest, errors.New("invalid alias")
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return nil, http.StatusNotFound, errors.New("unknown action")
	}
	response, err := ui.call(LauncherControlRequest{Operation: action, Alias: alias})
	if err != nil {
		return nil, 0, err
	}
	return response.Status, 0, nil
}

type webUIAddRequest struct {
	Alias       string `json:"alias"`
	Root        string `json:"root"`
	Engine      string `json:"engine"`
	Runtime     string `json:"runtime"`
	Supervision string `json:"supervision"`
	Autostart   bool   `json:"autostart"`
}

func (ui *WebUI) addInstance(r *http.Request) (any, int, error) {
	var request webUIAddRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	if request.Supervision == "" {
		request.Supervision = SupervisionManaged
	}
	if request.Engine == "" && request.Runtime == "" {
		// By default, the runtime package built by the setup (it can serve a web page), else the bare engine.
		if ui.options.Creation.runtime != "" {
			request.Runtime = ui.options.Creation.runtime
		} else if isFile(ui.options.Creation.engine) {
			request.Engine = ui.options.Creation.engine
		}
	}
	if (request.Engine == "") == (request.Runtime == "") {
		return nil, http.StatusBadRequest, errors.New("no engine found next to the launcher: give the runtime folder or the engine binary")
	}
	instance, err := ui.options.Store.Add(r.Context(), AddOptions{Alias: request.Alias, RootPath: request.Root,
		EnginePath: request.Engine, RuntimePath: request.Runtime, Supervision: request.Supervision, Autostart: request.Autostart})
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	return map[string]string{"alias": instance.Alias}, 0, nil
}

type webUILogs struct {
	Path  string   `json:"path,omitempty"`
	Lines []string `json:"lines"`
}

// logs returns the end of the most recent engine log of an instance, redacted again here
// because a running engine's log is only redacted when it is closed.
func (ui *WebUI) logs(r *http.Request) (any, int, error) {
	alias := r.PathValue("alias")
	if !validAlias(alias) {
		return nil, http.StatusBadRequest, errors.New("invalid alias")
	}
	directory := filepath.Join(ui.options.Store.Directory, "logs", alias)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return webUILogs{Lines: []string{}}, 0, nil
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	var newest string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".log") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	if newest == "" {
		return webUILogs{Lines: []string{}}, 0, nil
	}
	path := filepath.Join(directory, newest)
	file, err := os.Open(path)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	offset := int64(0)
	if info.Size() > webUILogTail {
		offset = info.Size() - webUILogTail
	}
	content := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(content, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, http.StatusInternalServerError, err
	}
	lines := strings.Split(strings.TrimRight(string(redactLauncherLog(content)), "\n"), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line was cut
	}
	if len(lines) > webUILogMaxRows {
		lines = lines[len(lines)-webUILogMaxRows:]
	}
	return webUILogs{Path: path, Lines: lines}, 0, nil
}

type webUIDiagnoseRequest struct {
	// Name labels the peer in the report; optional.
	Name string `json:"name"`
	// Address is where the peer is reached: host, host:port or an https URL.
	Address string `json:"address"`
}

// diagnosisTarget turns what the owner typed into the base URL to probe: a bare host or
// host:port means https. Whatever else is wrong with it (http, a user, a query) is reported
// by the diagnosis itself, with its stable code.
func diagnosisTarget(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" || len(address) > 255 {
		return "", errors.New("an address is required")
	}
	for _, r := range address {
		if r <= 0x20 || r == 0x7f {
			return "", errors.New("the address contains a space or a control character")
		}
	}
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	return address, nil
}

// diagnose probes the availability of a peer: name resolution, connection, certificate and
// HTTP answer. It does not depend on any instance of this launcher and does not check the
// peer's key (the identity and signature steps are not implemented yet). One probe at a time.
func (ui *WebUI) diagnose(r *http.Request) (any, int, error) {
	var request webUIDiagnoseRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	target, err := diagnosisTarget(request.Address)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if request.Name == "" {
		request.Name = request.Address
	}
	if len(request.Name) > 128 || strings.ContainsAny(request.Name, "\r\n\x00") {
		return nil, http.StatusBadRequest, errors.New("invalid peer name")
	}
	if !ui.diagnosing.TryLock() {
		return nil, http.StatusTooManyRequests, errors.New("a diagnosis is already running")
	}
	defer ui.diagnosing.Unlock()
	return diagnostic.Run(r.Context(), diagnostic.Input{PeerID: request.Name, Target: target,
		TLS: ui.options.DiagnosisTLS, TrustSource: "system"}), 0, nil
}

// mcpTarget resolves the registered instance and the WebSocket address its engine published.
func (ui *WebUI) mcpTarget(alias string) (Instance, string, error) {
	if !validAlias(alias) {
		return Instance{}, "", errors.New("invalid alias")
	}
	instance, err := ui.options.Store.Get(alias)
	if err != nil {
		return Instance{}, "", err
	}
	response, err := ui.call(LauncherControlRequest{Operation: "status", Alias: alias})
	if err != nil || response.Status == nil {
		return Instance{}, "", errors.New("the supervisor does not answer")
	}
	if response.Status.State != StateReady {
		return Instance{}, "", errors.New("the instance is not ready")
	}
	return instance, response.Status.UIWebSocket, nil
}

func (ui *WebUI) mcpStates(r *http.Request) (any, int, error) {
	instance, address, err := ui.mcpTarget(r.PathValue("alias"))
	if err != nil {
		return nil, http.StatusConflict, err
	}
	ui.engineMu.Lock()
	defer ui.engineMu.Unlock()
	states, err := ReadMCPStates(r.Context(), instance, address)
	if err != nil {
		return nil, 0, err
	}
	return states, 0, nil
}

type webUIMCPRequest struct {
	Profile   string `json:"profile"`
	Action    string `json:"action"`
	Confirmed bool   `json:"confirmed"`
}

func (ui *WebUI) mcpSet(r *http.Request) (any, int, error) {
	var request webUIMCPRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	instance, address, err := ui.mcpTarget(r.PathValue("alias"))
	if err != nil {
		return nil, http.StatusConflict, err
	}
	ui.engineMu.Lock()
	defer ui.engineMu.Unlock()
	if err := SetMCP(r.Context(), instance, address, request.Profile, request.Action, request.Confirmed); err != nil {
		return nil, http.StatusBadGateway, err
	}
	return map[string]bool{"ok": true}, 0, nil
}

type webUICreateRequest struct {
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

func (ui *WebUI) createInstance(r *http.Request) (any, int, error) {
	var request webUICreateRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	creation := ui.options.Creation
	if !creation.Available {
		return nil, http.StatusConflict, errors.New(creation.Reason)
	}
	if request.Parent == "" {
		request.Parent = creation.Parent
	}
	instance, err := ui.options.Store.CreateInstance(r.Context(), CreateInstanceOptions{Name: request.Name,
		ParentDirectory: request.Parent, TemplateDirectory: creation.template, SecurityBinary: creation.security, RuntimePath: creation.runtime, EnginePath: creation.engine})
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	result := map[string]any{"alias": instance.Alias, "root": instance.RootPath}
	if warnings := PreparePythonEnvironments(r.Context(), filepath.Dir(instance.RootPath)); len(warnings) > 0 {
		result["warnings"] = warnings
	}
	return result, 0, nil
}

type webUIAutostartRequest struct {
	Enabled bool `json:"enabled"`
}

// shutdown stops everything this launcher runs: the engines and web hosts of its managed instances, then the supervisor itself, which
// is the process that serves this page. The answer says which instances were running; the stopping follows it, since the page goes away.
func (ui *WebUI) shutdown(_ *http.Request) (any, int, error) {
	stopping := []string{}
	if response, err := ui.call(LauncherControlRequest{Operation: "list"}); err == nil {
		for _, status := range response.Statuses {
			if status.Owned && status.State != StateStopped {
				stopping = append(stopping, status.Alias)
			}
		}
	}
	if _, err := ui.call(LauncherControlRequest{Operation: "shutdown"}); err != nil {
		return nil, http.StatusServiceUnavailable, errors.New("the supervisor does not answer: it may already be stopped")
	}
	return map[string]any{"stopping": stopping}, 0, nil
}

func (ui *WebUI) setAutostart(r *http.Request) (any, int, error) {
	var request webUIAutostartRequest
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	instance, err := ui.options.Store.SetAutostart(r.PathValue("alias"), request.Enabled)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	return map[string]bool{"autostart": instance.Autostart}, 0, nil
}

func (ui *WebUI) publish(r *http.Request) (any, int, error) {
	var request struct {
		// Force publishes again although nothing changed.
		Force bool `json:"force"`
	}
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	alias := r.PathValue("alias")
	if !validAlias(alias) {
		return nil, http.StatusBadRequest, errors.New("invalid alias")
	}
	instance, err := ui.options.Store.Get(alias)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if err := ui.startPublish(instance, request.Force); err != nil {
		return nil, http.StatusConflict, err
	}
	return ui.publisher.status(alias), 0, nil
}

func (ui *WebUI) useRuntimeHandler(r *http.Request) (any, int, error) {
	var request struct{}
	if err := decodeWebUIBody(r, &request); err != nil {
		return nil, http.StatusBadRequest, err
	}
	alias := r.PathValue("alias")
	if !validAlias(alias) {
		return nil, http.StatusBadRequest, errors.New("invalid alias")
	}
	if err := ui.useRuntime(alias); err != nil {
		return nil, http.StatusBadRequest, err
	}
	return map[string]bool{"ok": true}, 0, nil
}
