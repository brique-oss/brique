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

package wrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/net/websocket"
)

const maxControlMessageBytes = 4 << 20

// TransportClient manages the WebSocket connection to the Brique engine boundary.
type TransportClient struct {
	url  string
	conn *websocket.Conn
	mu   sync.Mutex
}

func NewTransportClient(url string) *TransportClient {
	return &TransportClient{url: url}
}

func (t *TransportClient) Connect(_ context.Context) error {
	conn, err := websocket.Dial(t.url, "", "http://localhost")
	if err != nil {
		return fmt.Errorf("transport connect: %w", err)
	}
	t.conn = conn
	return nil
}

func (t *TransportClient) Send(msg any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		return fmt.Errorf("transport: not connected")
	}
	data, err := marshalControlMessage(msg)
	if err != nil {
		return err
	}
	return websocket.Message.Send(t.conn, string(data))
}

func marshalControlMessage(msg any) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("transport marshal: %w", err)
	}
	if len(data) > maxControlMessageBytes {
		return nil, fmt.Errorf("transport: control message is %d bytes, limit is %d; store large payloads in Matter substance", len(data), maxControlMessageBytes)
	}
	return data, nil
}

// ReceiveLoop calls handler for each received raw JSON message until the connection closes.
func (t *TransportClient) ReceiveLoop(ctx context.Context, handler func(raw []byte)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var raw string
		if err := websocket.Message.Receive(t.conn, &raw); err != nil {
			if isExpectedClose(err) {
				return nil
			}
			return err
		}
		handler([]byte(raw))
	}
}

func (t *TransportClient) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
}

func isExpectedClose(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "use of closed") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused")
}

// ResolveWrapperURL reads context.json from ctxDir and returns the WebSocket URL
// for the named wrapper interface.
func ResolveWrapperURL(ctxDir, wrapperName string) (string, error) {
	data, err := os.ReadFile(ctxDir + "/context.json")
	if err != nil {
		return "", fmt.Errorf("resolve wrapper url: %w", err)
	}
	var doc struct {
		Brique struct {
			EngineConfig struct {
				Communication struct {
					Interfaces []struct {
						Name   string `json:"name"`
						Type   string `json:"type"`
						Driver string `json:"driver"`
						Config struct {
							Addr string `json:"addr"`
							Path string `json:"path"`
						} `json:"config"`
					} `json:"interfaces"`
				} `json:"communication"`
			} `json:"engine_config"`
		} `json:"brique"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("resolve wrapper url parse: %w", err)
	}
	for _, iface := range doc.Brique.EngineConfig.Communication.Interfaces {
		if iface.Type != "wrapper" || iface.Name != wrapperName || iface.Driver != "ws" {
			continue
		}
		addr := strings.TrimSpace(iface.Config.Addr)
		path := iface.Config.Path
		if path == "" {
			path = "/ws"
		}
		if baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("BRIQUE_WS_URL")), "/"); baseURL != "" {
			return baseURL + path, nil
		}
		if addr == "" {
			addr = strings.TrimSpace(os.Getenv("BRIQUE_WS_ADDR"))
		}
		if addr == "" {
			return "", fmt.Errorf("shared websocket listener addr not provided")
		}
		host := "127.0.0.1"
		if strings.HasPrefix(addr, ":") {
			return fmt.Sprintf("ws://%s%s%s", host, addr, path), nil
		}
		h, port, err := net.SplitHostPort(addr)
		if err != nil {
			return "", fmt.Errorf("resolve wrapper url addr: %w", err)
		}
		if h == "" || h == "0.0.0.0" {
			h = host
		}
		return fmt.Sprintf("ws://%s:%s%s", h, port, path), nil
	}
	return "", fmt.Errorf("wrapper interface %q not found in context.json", wrapperName)
}

// isRefused checks for connection refused — used at startup to exit cleanly.
func isRefused(err error) bool {
	var opErr *net.OpError
	if err == nil {
		return false
	}
	if ne, ok := err.(*net.OpError); ok {
		opErr = ne
		_ = opErr
	}
	return strings.Contains(err.Error(), "connection refused")
}
