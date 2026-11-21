//go:build unix

package browserbridge

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEngineIPCListenerIsPrivateDiscoverableAndRemoved(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "brique-browser-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	discoveryKey := "brd1_" + base64.RawURLEncoding.EncodeToString(make([]byte, 18))
	sink := newIPCTestSink()
	service, err := StartEngineIPCListener(filepath.Clean(directory), discoveryKey, ipcTestGeneration, sink)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	info, err := os.Stat(endpoint.Address)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o", info.Mode().Perm())
	}
	client, err := DialEngineIPC(endpoint, EngineIPCClientOptions{Generation: ipcTestGeneration,
		Deliver: func(string, []byte) {}, Invalidate: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{SessionID: "bbs1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ChannelID: "bbc1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", Origin: "https://ui.example",
		DocumentID: "document", UIName: "root"}
	if err := client.Open(binding); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("listener close did not disconnect client")
	}
	if _, err := os.Stat(endpoint.Address); !os.IsNotExist(err) {
		t.Fatalf("socket remained after close: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed[binding.ChannelID] != 1 {
		t.Fatalf("sink close count = %d", sink.closed[binding.ChannelID])
	}
}
