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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Diagnostics of the WebSocket routes. It decides nothing and changes nothing: it records, one line each, the
// opening of a connection, its replacement by another, and its closing with the reason, and it follows how full the
// ingress channel of the communication family gets. A connection closed without a word is the hardest failure to
// find: the peer only sees "no close frame received or sent".
//
// The lines go to the file named by BRIQUE_CONNECTION_LOG (default: brique_ws_connections.log in the temporary
// directory) and to the error stream.

var connectionDiagnostics struct {
	mu     sync.Mutex
	opened bool
	file   *os.File
}

func connectionDiagnosticsPath() string {
	if path := os.Getenv("BRIQUE_CONNECTION_LOG"); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "brique_ws_connections.log")
}

func connectionLog(format string, args ...any) {
	line := time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + " " + fmt.Sprintf(format, args...) + "\n"
	connectionDiagnostics.mu.Lock()
	defer connectionDiagnostics.mu.Unlock()
	if !connectionDiagnostics.opened {
		connectionDiagnostics.opened = true
		if file, err := os.OpenFile(connectionDiagnosticsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			connectionDiagnostics.file = file
		}
	}
	if connectionDiagnostics.file != nil {
		_, _ = connectionDiagnostics.file.WriteString(line)
	}
	_, _ = fmt.Fprint(os.Stderr, "brique ws: "+line)
}

// routeLabel names a route in the diagnostics: its path, its interface and its kind.
func (s *webSocketRouteState) routeLabel() string {
	kind := "ui"
	if s.route.Wrapper {
		kind = "wrapper"
	}
	return fmt.Sprintf("route=%s iface=%s kind=%s", s.route.Path, s.route.IfaceName, kind)
}

// noteIngressFill records how full the ingress channel is just before an item is offered to it, keeps the highest
// value seen on this route, and says so (at most five times a second) when it is at three quarters or more.
func (s *webSocketRouteState) noteIngressFill() (fill, capacity int, high int64) {
	fill, capacity = len(s.route.Ingress), cap(s.route.Ingress)
	for {
		current := atomic.LoadInt64(&s.ingressHigh)
		if int64(fill) <= current || atomic.CompareAndSwapInt64(&s.ingressHigh, current, int64(fill)) {
			break
		}
	}
	high = atomic.LoadInt64(&s.ingressHigh)
	if capacity > 0 && fill*4 >= capacity*3 {
		now := time.Now().UnixMilli()
		last := atomic.LoadInt64(&s.lastFillLog)
		if now-last >= 200 && atomic.CompareAndSwapInt64(&s.lastFillLog, last, now) {
			connectionLog("high_fill %s ingress=%d/%d high=%d", s.routeLabel(), fill, capacity, high)
		}
	}
	return fill, capacity, high
}
