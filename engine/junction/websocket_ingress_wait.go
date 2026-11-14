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
	"sync/atomic"
	"time"
)

// ingressWait is how long a read loop waits for a place in the ingress channel of the communication family.
func (s *webSocketRouteState) ingressWait() time.Duration {
	if s.parent != nil && s.parent.cfg.IngressWait > 0 {
		return s.parent.cfg.IngressWait
	}
	return 5 * time.Second
}

// waitForIngress offers the item to the full ingress channel until a place frees up or the wait ends. It returns whether the item was taken.
// The read loop that calls it reads nothing from its socket meanwhile, so the peer is slowed by its own TCP window instead of losing its
// connection. A wait of 100 ms or more is written in the connection diagnostics (at most five lines a second).
func (s *webSocketRouteState) waitForIngress(item InterfaceIngressItem) bool {
	started := time.Now()
	timer := time.NewTimer(s.ingressWait())
	defer timer.Stop()
	select {
	case s.route.Ingress <- item:
		if waited := time.Since(started); waited >= 100*time.Millisecond {
			now := time.Now().UnixMilli()
			last := atomic.LoadInt64(&s.lastWaitLog)
			if now-last >= 200 && atomic.CompareAndSwapInt64(&s.lastWaitLog, last, now) {
				connectionLog("ingress_wait %s waited=%s", s.routeLabel(), waited.Round(time.Millisecond))
			}
		}
		return true
	case <-timer.C:
		return false
	}
}
