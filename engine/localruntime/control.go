package localruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"brique_engine/contracts/buildinfo"
	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/contracts/directoryobject"
	"brique_engine/engine/browserbridge"
	"brique_engine/platform/listener"
)

const (
	maxControlMessageBytes = 64 << 10
	maxControlConnections  = 16
	controlDeadline        = 5 * time.Second
	// diagnoseDeadline covers a connection diagnosis, which opens a real connection to a peer.
	diagnoseDeadline = 30 * time.Second
	maxDiagnoseName  = 128

	OperationStatus     = "status"
	OperationVersion    = "version"
	OperationRoot       = "root"
	OperationGeneration = "generation"
	OperationStop       = "stop"
	// OperationDiagnoseConnection probes one declared peer endpoint. It is reachable only
	// through this owner-only socket: no message, signed or not, can start it.
	OperationDiagnoseConnection = "diagnose_connection"
)

type ControlRequest struct {
	Format     string `json:"format"`
	Generation string `json:"generation"`
	Operation  string `json:"operation"`
	// Peer and Endpoint name a declared peer and one of its endpoints for a connection
	// diagnosis. They are names looked up in the signed policy, never addresses.
	Peer     string `json:"peer,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

type ControlResponse struct {
	Format     string                      `json:"format"`
	Generation string                      `json:"generation"`
	OK         bool                        `json:"ok"`
	ErrorCode  string                      `json:"error_code,omitempty"`
	State      string                      `json:"state,omitempty"`
	PID        int                         `json:"pid,omitempty"`
	RootPath   string                      `json:"root_path,omitempty"`
	RootObject *directoryobject.ID         `json:"root_object,omitempty"`
	Build      *buildinfo.Info             `json:"build,omitempty"`
	Diagnosis  *connectiondiagnosis.Result `json:"diagnosis,omitempty"`
}

type ControlOptions struct {
	DiscoveryDirectory string
	RootPath           string
	RootObject         directoryobject.ID
	Generation         string
	Now                func() time.Time
	// Diagnose answers OperationDiagnoseConnection; without it the operation is refused.
	Diagnose func(ctx context.Context, peer, endpoint string) connectiondiagnosis.Result
}

type ControlServer struct {
	mu         sync.RWMutex
	descriptor EngineRuntimeDescriptor
	directory  string
	listener   net.Listener
	cleanup    func() error
	stop       chan struct{}
	stopOnce   sync.Once
	closeOnce  sync.Once
	closed     chan struct{}
	sem        chan struct{}
	wg         sync.WaitGroup
	closeErr   error
	diagnose   func(ctx context.Context, peer, endpoint string) connectiondiagnosis.Result
}

func StartControl(options ControlOptions) (*ControlServer, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := PrepareDiscoveryDirectory(options.DiscoveryDirectory); err != nil {
		return nil, err
	}
	key, err := DiscoveryKey(options.RootObject)
	if err != nil {
		return nil, err
	}
	listener, endpoint, cleanup, err := listenLocalControl(options.DiscoveryDirectory, key)
	if err != nil {
		return nil, err
	}
	descriptor, err := NewDescriptor(options.RootPath, options.RootObject, options.Generation, StateStarting, endpoint, options.Now())
	if err != nil {
		_ = listener.Close()
		_ = cleanup()
		return nil, err
	}
	if err := PublishDescriptor(options.DiscoveryDirectory, descriptor); err != nil {
		_ = listener.Close()
		_ = cleanup()
		return nil, err
	}
	server := &ControlServer{
		descriptor: descriptor, directory: options.DiscoveryDirectory,
		listener: listener, cleanup: cleanup, stop: make(chan struct{}),
		closed: make(chan struct{}), sem: make(chan struct{}, maxControlConnections),
		diagnose: options.Diagnose,
	}
	server.wg.Add(1)
	go server.acceptLoop()
	return server, nil
}

func (server *ControlServer) Descriptor() EngineRuntimeDescriptor {
	server.mu.RLock()
	defer server.mu.RUnlock()
	return server.descriptor
}

func (server *ControlServer) StopRequested() <-chan struct{} { return server.stop }

func (server *ControlServer) SetState(state string) error {
	server.mu.Lock()
	descriptor := server.descriptor
	descriptor.State = state
	descriptor.PublishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := descriptor.Validate(); err != nil {
		server.mu.Unlock()
		return err
	}
	if err := PublishDescriptor(server.directory, descriptor); err != nil {
		server.mu.Unlock()
		return err
	}
	server.descriptor = descriptor
	server.mu.Unlock()
	return nil
}

func (server *ControlServer) SetUIWebSocketAddress(address string) error {
	server.mu.Lock()
	descriptor := server.descriptor
	descriptor.UIWebSocket = address
	descriptor.PublishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := descriptor.Validate(); err != nil {
		server.mu.Unlock()
		return err
	}
	if err := PublishDescriptor(server.directory, descriptor); err != nil {
		server.mu.Unlock()
		return err
	}
	server.descriptor = descriptor
	server.mu.Unlock()
	return nil
}

func (server *ControlServer) SetPublicWebSocketAddress(address string) error {
	server.mu.Lock()
	descriptor := server.descriptor
	descriptor.PublicWebSocket = address
	descriptor.PublishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := descriptor.Validate(); err != nil {
		server.mu.Unlock()
		return err
	}
	if err := PublishDescriptor(server.directory, descriptor); err != nil {
		server.mu.Unlock()
		return err
	}
	server.descriptor = descriptor
	server.mu.Unlock()
	return nil
}

func (server *ControlServer) SetBrowserBridgeEndpoint(endpoint browserbridge.EngineIPCEndpoint) error {
	if err := endpoint.Validate(); err != nil {
		return err
	}
	server.mu.Lock()
	descriptor := server.descriptor
	descriptor.BrowserBridge = &endpoint
	descriptor.PublishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := descriptor.Validate(); err != nil {
		server.mu.Unlock()
		return err
	}
	if err := PublishDescriptor(server.directory, descriptor); err != nil {
		server.mu.Unlock()
		return err
	}
	server.descriptor = descriptor
	server.mu.Unlock()
	return nil
}

func (server *ControlServer) acceptLoop() {
	defer server.wg.Done()
	backoff := listener.AcceptBackoff{Name: "engine local control"}
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if !backoff.Failed(err, server.closed) {
				return
			}
			continue
		}
		backoff.Succeeded()
		select {
		case server.sem <- struct{}{}:
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				defer func() { <-server.sem }()
				server.handle(connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (server *ControlServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(controlDeadline))
	reader := bufio.NewReaderSize(connection, maxControlMessageBytes+1)
	raw, err := reader.ReadSlice('\n')
	if err != nil || len(raw) > maxControlMessageBytes {
		return
	}
	request, err := decodeControlRequest(raw)
	if err != nil {
		_ = writeControlResponse(connection, ControlResponse{Format: LocalControlProtocol, OK: false, ErrorCode: "invalid_request"})
		return
	}
	descriptor := server.Descriptor()
	response := ControlResponse{Format: LocalControlProtocol, Generation: descriptor.Generation}
	if request.Generation != descriptor.Generation {
		response.ErrorCode = "stale_generation"
		_ = writeControlResponse(connection, response)
		return
	}
	response.OK = true
	switch request.Operation {
	case OperationStatus:
		response.State, response.PID = descriptor.State, descriptor.PID
	case OperationVersion:
		build := descriptor.Build
		response.Build = &build
	case OperationRoot:
		rootObject := descriptor.RootObject
		response.RootPath, response.RootObject = descriptor.RootPath, &rootObject
	case OperationGeneration:
	case OperationStop:
	case OperationDiagnoseConnection:
		if server.diagnose == nil {
			response.OK, response.ErrorCode = false, "unsupported_operation"
			break
		}
		if !validDiagnosisName(request.Peer) || request.Endpoint != "" && !validDiagnosisName(request.Endpoint) {
			response.OK, response.ErrorCode = false, "invalid_request"
			break
		}
		_ = connection.SetDeadline(time.Now().Add(diagnoseDeadline))
		ctx, cancel := context.WithTimeout(context.Background(), diagnoseDeadline-2*time.Second)
		result := server.diagnose(ctx, request.Peer, request.Endpoint)
		cancel()
		response.Diagnosis = &result
	default:
		response.OK, response.ErrorCode = false, "unknown_operation"
	}
	if err := writeControlResponse(connection, response); err == nil && request.Operation == OperationStop && response.OK {
		server.stopOnce.Do(func() { close(server.stop) })
	}
}

// validDiagnosisName accepts a principal or endpoint name: printable ASCII without
// separators that make it look like an address or a path.
func validDiagnosisName(value string) bool {
	if value == "" || len(value) > maxDiagnoseName {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e || r == '/' || r == '\\' || r == ':' {
			return false
		}
	}
	return true
}

func decodeControlRequest(raw []byte) (ControlRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request ControlRequest
	if err := decoder.Decode(&request); err != nil || request.Format != LocalControlProtocol || !ValidGeneration(request.Generation) || request.Operation == "" {
		return ControlRequest{}, errors.New("invalid local control request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ControlRequest{}, errors.New("invalid trailing local control data")
	}
	return request, nil
}

func writeControlResponse(destination io.Writer, response ControlResponse) error {
	raw, err := json.Marshal(response)
	if err != nil || len(raw) >= maxControlMessageBytes {
		return errors.New("encode local control response")
	}
	raw = append(raw, '\n')
	_, err = destination.Write(raw)
	return err
}

func (server *ControlServer) Close() error {
	server.closeOnce.Do(func() {
		close(server.closed)
		server.closeErr = server.listener.Close()
		server.wg.Wait()
		descriptor := server.Descriptor()
		if err := RemoveOwnedDescriptor(server.directory, descriptor.DiscoveryKey, descriptor.Generation); err != nil && server.closeErr == nil {
			server.closeErr = err
		}
		if err := server.cleanup(); err != nil && server.closeErr == nil {
			server.closeErr = err
		}
	})
	return server.closeErr
}

func CallControl(descriptor EngineRuntimeDescriptor, request ControlRequest) (ControlResponse, error) {
	if err := descriptor.Validate(); err != nil {
		return ControlResponse{}, err
	}
	deadline := controlDeadline
	if request.Operation == OperationDiagnoseConnection {
		deadline = diagnoseDeadline
	}
	connection, err := dialLocalControl(descriptor.Control, controlDeadline)
	if err != nil {
		return ControlResponse{}, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(deadline))
	raw, err := json.Marshal(request)
	if err != nil || len(raw) >= maxControlMessageBytes {
		return ControlResponse{}, errors.New("encode local control request")
	}
	if _, err := connection.Write(append(raw, '\n')); err != nil {
		return ControlResponse{}, err
	}
	reader := bufio.NewReaderSize(connection, maxControlMessageBytes+1)
	responseRaw, err := reader.ReadSlice('\n')
	if err != nil || len(responseRaw) > maxControlMessageBytes {
		return ControlResponse{}, errors.New("invalid local control response")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseRaw))
	decoder.DisallowUnknownFields()
	var response ControlResponse
	if err := decoder.Decode(&response); err != nil || response.Format != LocalControlProtocol || response.Generation != descriptor.Generation {
		return ControlResponse{}, errors.New("invalid local control response")
	}
	return response, nil
}
