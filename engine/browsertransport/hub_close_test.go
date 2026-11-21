package browsertransport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// The read loop closes the socket itself when the peer goes away, possibly just before an explicit
// Close. Closing a route whose socket is already gone is not a failure.
func TestClosingARouteWhoseSocketIsAlreadyClosedIsNotAnError(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			defer connection.Close()
			_, _, _ = connection.ReadMessage()
		}
	}))
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.UnderlyingConn().Close() // what the read loop does when the peer disappears

	route := &HubRoute{wire: &WireSession{connection: connection, done: make(chan struct{})}}
	if err := route.Close(); err != nil {
		t.Fatalf("closing a route whose socket was already closed = %v, want nil", err)
	}
	if err := route.Close(); err != nil {
		t.Fatalf("a second close = %v, want nil", err)
	}
}
