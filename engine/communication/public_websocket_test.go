/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"

	"github.com/gorilla/websocket"
)

func TestPublicWebSocketIsolatesTwoClientsAndManufacturesAuthority(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 8)
	world.loop.browserUI = make(map[string]func(circulation.Message) error)
	world.loop.browserPublicUI = make(map[string]browserPublicUIRoute)
	var idMu sync.Mutex
	nextID := 0
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		UIName:         func(*http.Request) (string, bool) { return "root", true },
		AllowedHosts:   []string{"pending"},
		AllowedOrigins: []string{"https://public.example"},
		NewChannelID: func() (string, error) {
			idMu.Lock()
			defer idMu.Unlock()
			nextID++
			return fmt.Sprintf("bpw1_channel_%d", nextID), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	handler.options.AllowedHosts = []string{server.Listener.Addr().String()}

	clientA := dialPublicWebSocket(t, server, "https://public.example")
	defer clientA.Close()
	clientB := dialPublicWebSocket(t, server, "https://public.example")
	defer clientB.Close()

	for index, client := range []*websocket.Conn{clientA, clientB} {
		request := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
			IntentionID: fmt.Sprintf("123e4567-e89b-42d3-a456-42661417400%d", index), AwaitResponse: true,
			To:       circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
			From:     circulation.Address{Context: "/forged", Type: "admin", Cap: "control"},
			Identity: circulation.Identity{ID: "forged", Kind: "instance"},
		}}
		payload, _ := json.Marshal(request)
		if err := client.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
	}
	items := []IngressItem{<-world.loop.ingress, <-world.loop.ingress}
	if items[0].Msg.Intention.From.Context == items[1].Msg.Intention.From.Context {
		t.Fatal("two public clients shared a channel authority")
	}
	for _, item := range items {
		if item.Msg.Intention.Identity != (circulation.Identity{}) || !strings.HasSuffix(string(item.Msg.Intention.From.Context), "/root") {
			t.Fatalf("untrusted browser fields crossed ingress: %+v", item.Msg.Intention)
		}
	}

	for index, item := range items {
		world.loop.routeEgressRoot(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
			IntentionID: item.Msg.Intention.IntentionID,
			To:          circulation.Address{Context: item.Msg.Intention.From.Context},
			Status:      circulation.ValueStatusOK,
		}})
		client := clientA
		if strings.Contains(string(item.Msg.Intention.From.Context), "channel_2") {
			client = clientB
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, err := client.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var response circulation.Message
		if err := json.Unmarshal(payload, &response); err != nil || response.Response.IntentionID != item.Msg.Intention.IntentionID {
			t.Fatalf("response %d = %+v, %v", index, response, err)
		}
	}
}

func TestPublicWebProfileFixtureMatchesRuntimeLimits(t *testing.T) {
	raw, err := os.ReadFile("testdata/public_web_profile_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Format                      string `json:"format"`
		ControlFrameMaxBytes        int    `json:"control_frame_max_bytes"`
		ConnectionsGlobal           int    `json:"connections_global"`
		ConnectionsPerAddress       int    `json:"connections_per_address"`
		RatePerChannel              int    `json:"intentions_per_second_per_channel"`
		BurstPerChannel             int    `json:"intentions_burst_per_channel"`
		RateGlobal                  int    `json:"intentions_per_second_global"`
		BurstGlobal                 int    `json:"intentions_burst_global"`
		OutgoingMessages            int    `json:"outgoing_messages_per_connection"`
		OutgoingBytes               int    `json:"outgoing_bytes_per_connection"`
		WriteTimeoutMilliseconds    int    `json:"write_timeout_ms"`
		ReadIdleTimeoutMilliseconds int    `json:"read_idle_timeout_ms"`
		PingIntervalMilliseconds    int    `json:"ping_interval_ms"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Format != "brique.public-web-profile/1" ||
		profile.ControlFrameMaxBytes != maxPublicWebFrameBytes ||
		profile.ConnectionsGlobal != maxPublicWebConnections ||
		profile.ConnectionsPerAddress != publicWebPerAddressMax ||
		profile.RatePerChannel != publicWebChannelRate || profile.BurstPerChannel != publicWebChannelBurst ||
		profile.RateGlobal != publicWebGlobalRate || profile.BurstGlobal != publicWebGlobalBurst ||
		profile.OutgoingMessages != publicWebOutgoingMessages || profile.OutgoingBytes != publicWebOutgoingBytes ||
		time.Duration(profile.WriteTimeoutMilliseconds)*time.Millisecond != publicWebWriteTimeout ||
		time.Duration(profile.ReadIdleTimeoutMilliseconds)*time.Millisecond != publicWebReadIdleTimeout ||
		time.Duration(profile.PingIntervalMilliseconds)*time.Millisecond != publicWebPingInterval {
		t.Fatalf("public web profile does not match runtime limits: %#v", profile)
	}
}

func TestPublicWebSocketIdleTimeoutRequiresPongAndReleasesQuota(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 4)
	world.loop.browserUI = make(map[string]func(circulation.Message) error)
	world.loop.browserPublicUI = make(map[string]browserPublicUIRoute)
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		UIName:       func(*http.Request) (string, bool) { return "main", true },
		AllowedHosts: []string{"pending"}, AllowedOrigins: []string{"https://public.example"},
		MaxConnections: 2, MaxPerAddress: 1, ReadIdleTimeout: 250 * time.Millisecond, PingInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	handler.options.AllowedHosts = []string{server.Listener.Addr().String()}
	dial := func() (*websocket.Conn, *http.Response, error) {
		dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Subprotocols: []string{PublicWebSocketSubprotocol}}
		return dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https"),
			http.Header{"Origin": []string{"https://public.example"}})
	}
	silent, response, err := dial()
	if err != nil {
		t.Fatalf("silent connection: status=%v err=%v", responseStatus(response), err)
	}
	defer silent.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		replacement, _, replacementErr := dial()
		if replacementErr == nil {
			replacement.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("silent connection did not release its per-address quota")
		}
		time.Sleep(20 * time.Millisecond)
	}

	responsive, response, err := dial()
	if err != nil {
		t.Fatalf("responsive connection: status=%v err=%v", responseStatus(response), err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, readErr := responsive.ReadMessage(); readErr != nil {
				return
			}
		}
	}()
	time.Sleep(3 * handler.options.ReadIdleTimeout)
	blocked, response, err := dial()
	if blocked != nil {
		blocked.Close()
	}
	if err == nil || responseStatus(response) != http.StatusServiceUnavailable {
		t.Fatalf("responsive connection lost quota ownership: status=%v err=%v", responseStatus(response), err)
	}
	responsive.Close()
	<-done
}

func TestPublicWebSocketRejectsOriginHostAndPerAddressOverflow(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 4)
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		UIName:         func(*http.Request) (string, bool) { return "root", true },
		AllowedHosts:   []string{"pending"},
		AllowedOrigins: []string{"https://public.example"},
		MaxConnections: 2, MaxPerAddress: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	handler.options.AllowedHosts = []string{server.Listener.Addr().String()}

	if connection, response, err := dialPublicWebSocketResult(server, "https://evil.example", ""); err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		if connection != nil {
			connection.Close()
		}
		t.Fatalf("wrong origin: response=%v err=%v", responseStatus(response), err)
	}
	if connection, response, err := dialPublicWebSocketResult(server, "https://public.example", "wrong.example"); err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		if connection != nil {
			connection.Close()
		}
		t.Fatalf("wrong host: response=%v err=%v", responseStatus(response), err)
	}
	first := dialPublicWebSocket(t, server, "https://public.example")
	defer first.Close()
	if connection, response, err := dialPublicWebSocketResult(server, "https://public.example", ""); err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		if connection != nil {
			connection.Close()
		}
		t.Fatalf("per-address overflow: response=%v err=%v", responseStatus(response), err)
	}
}

func TestPublicWebTokenBucketHasExactBurstAndRefill(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	bucket := newPublicWebTokenBucket(10, 20, now)
	for index := 0; index < 20; index++ {
		if !bucket.take(now) {
			t.Fatalf("burst item %d refused", index)
		}
	}
	if bucket.take(now) {
		t.Fatal("burst overflow accepted")
	}
	if !bucket.take(now.Add(100*time.Millisecond)) || bucket.take(now.Add(100*time.Millisecond)) {
		t.Fatal("rate refill is not exactly one token")
	}
}

func TestPublicWebSocketSlowConsumerIsDisconnectedAtQueueBoundary(t *testing.T) {
	now := time.Now()
	handler := &PublicWebSocketHandler{connections: make(map[string]*publicWebSocketConnection)}
	connection := &publicWebSocketConnection{out: make(chan []byte, 1), done: make(chan struct{}),
		limiter: newPublicWebTokenBucket(publicWebChannelRate, publicWebChannelBurst, now)}
	handler.connections["channel"] = connection
	if err := handler.deliver("channel", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := handler.deliver("channel", []byte("second")); err == nil {
		t.Fatal("full outgoing queue accepted another message")
	}
	select {
	case <-connection.done:
	default:
		t.Fatal("slow consumer was not disconnected")
	}
}

func TestPublicWebSocketAbruptCloseCleansChannel(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 1)
	const channelID = "bpw1_abrupt_close"
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		UIName:         func(*http.Request) (string, bool) { return "root", true },
		AllowedHosts:   []string{"pending"},
		AllowedOrigins: []string{"https://public.example"},
		NewChannelID:   func() (string, error) { return channelID, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	handler.options.AllowedHosts = []string{server.Listener.Addr().String()}
	client := dialPublicWebSocket(t, server, "https://public.example")
	deadline := time.Now().Add(2 * time.Second)
	for !world.registry.UIChannelActive("public_web", channelID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !world.registry.UIChannelActive("public_web", channelID) {
		t.Fatal("channel did not become active")
	}
	_ = client.UnderlyingConn().Close()
	for world.registry.UIChannelActive("public_web", channelID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if world.registry.UIChannelActive("public_web", channelID) {
		t.Fatal("abruptly closed channel was not cleaned")
	}
}

func TestPublicWebServerPublishesLoopbackEndpointAndRoutes(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 1)
	server, err := StartPublicWebServer(world.loop, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	endpoint := server.Endpoint()
	origin := "http://" + strings.TrimSuffix(strings.TrimPrefix(endpoint, "ws://"), publicWebSocketPath)
	dialer := websocket.Dialer{Subprotocols: []string{PublicWebSocketSubprotocol}}
	connection, response, err := dialer.Dial(endpoint+"root", http.Header{
		"Origin":                       []string{origin},
		publicWebObservedAddressHeader: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("dial server endpoint: response=%v err=%v", responseStatus(response), err)
	}
	defer connection.Close()
	request := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000",
		To:          circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
	}}
	payload, _ := json.Marshal(request)
	if err := connection.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case item := <-world.loop.ingress:
		if !strings.HasPrefix(string(item.Msg.Intention.From.Context), "@public_web:/bpw1_") {
			t.Fatalf("public server source = %q", item.Msg.Intention.From.Context)
		}
		parts := strings.Split(strings.TrimPrefix(string(item.Msg.Intention.From.Context), "@public_web:/"), "/")
		if len(parts) != 2 || !world.registry.UIChannelActive("public_web", parts[0]) {
			t.Fatalf("active public channel = %q", item.Msg.Intention.From.Context)
		}
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		if world.registry.UIChannelActive("public_web", parts[0]) {
			t.Fatal("server shutdown left its public channel active")
		}
		if err := server.Close(); err != nil {
			t.Fatalf("idempotent close = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("public server did not route the intention")
	}
}

func dialPublicWebSocket(t testing.TB, server *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	connection, response, err := dialPublicWebSocketResult(server, origin, "")
	if err != nil {
		t.Fatalf("dial public websocket: response=%v err=%v", responseStatus(response), err)
	}
	return connection
}

func dialPublicWebSocketResult(server *httptest.Server, origin, host string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Subprotocols: []string{PublicWebSocketSubprotocol}}
	header := http.Header{"Origin": []string{origin}}
	if host != "" {
		header.Set("Host", host)
	}
	url := "wss" + strings.TrimPrefix(server.URL, "https")
	return dialer.Dial(url, header)
}

func responseStatus(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}

func TestPublicWebProxyObservedAddressIsStrictAndSeparatesQuotas(t *testing.T) {
	world := newRestrictedWorld(t)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 4)
	world.loop.browserUI = make(map[string]func(circulation.Message) error)
	world.loop.browserPublicUI = make(map[string]browserPublicUIRoute)
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		UIName:       func(*http.Request) (string, bool) { return "main", true },
		AllowedHosts: []string{"pending"}, AllowedOrigins: []string{"https://public.example"},
		ObservedAddress: observedPublicWebProxyAddress, MaxConnections: 3, MaxPerAddress: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	handler.options.AllowedHosts = []string{server.Listener.Addr().String()}

	dial := func(address string) (*websocket.Conn, *http.Response, error) {
		dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Subprotocols: []string{PublicWebSocketSubprotocol}}
		header := http.Header{"Origin": []string{"https://public.example"}}
		if address != "" {
			header.Set(publicWebObservedAddressHeader, address)
		}
		return dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https"), header)
	}
	first, response, err := dial("198.51.100.10")
	if err != nil {
		t.Fatalf("first address: status=%v err=%v", responseStatus(response), err)
	}
	defer first.Close()
	duplicate, response, err := dial("198.51.100.10")
	if duplicate != nil {
		duplicate.Close()
	}
	if err == nil || responseStatus(response) != http.StatusServiceUnavailable {
		t.Fatalf("duplicate address: status=%v err=%v", responseStatus(response), err)
	}
	second, response, err := dial("198.51.100.11")
	if err != nil {
		t.Fatalf("second address: status=%v err=%v", responseStatus(response), err)
	}
	second.Close()
	missing, response, err := dial("")
	if missing != nil {
		missing.Close()
	}
	if err == nil || responseStatus(response) != http.StatusServiceUnavailable {
		t.Fatalf("missing observed address: status=%v err=%v", responseStatus(response), err)
	}
}

func BenchmarkPublicWebSocketRoundTrip(b *testing.B) {
	world := newRestrictedWorld(b)
	world.loop.frame.CtxId = shared.RootContextID
	world.loop.ingress = make(chan IngressItem, 16)
	var ticks atomic.Int64
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	handler, err := NewPublicWebSocketHandler(PublicWebSocketOptions{
		Loop: world.loop, Admit: world.runtime.AdmitPublicWeb,
		UIName:         func(*http.Request) (string, bool) { return "main", true },
		AllowedHosts:   []string{"pending"},
		AllowedOrigins: []string{"https://public.example"},
		Now: func() time.Time {
			return base.Add(time.Duration(ticks.Add(1)) * 100 * time.Millisecond)
		},
		NewChannelID: func() (string, error) { return "bpw1_benchmark_socket", nil },
	})
	if err != nil {
		b.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	handler.options.AllowedHosts = []string{server.Listener.Addr().String()}
	connection := dialPublicWebSocket(b, server, "https://public.example")
	defer connection.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case item := <-world.loop.ingress:
				world.loop.routeEgressRoot(circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
					IntentionID: item.Msg.Intention.IntentionID,
					To:          circulation.Address{Context: item.Msg.Intention.From.Context},
					Status:      circulation.ValueStatusOK,
				}})
			case <-stop:
				return
			}
		}
	}()
	payload, err := json.Marshal(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "123e4567-e89b-42d3-a456-426614174000", AwaitResponse: true,
		To: circulation.Address{Context: "/root/catalog", Type: circulation.ValueTypeMatter, Cap: "matter.read"},
	}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		if err := connection.WriteMessage(websocket.TextMessage, payload); err != nil {
			b.Fatal(err)
		}
		if _, _, err := connection.ReadMessage(); err != nil {
			b.Fatal(err)
		}
	}
}
