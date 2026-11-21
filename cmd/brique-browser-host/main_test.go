package main

import (
	"testing"

	"brique_engine/launcher"
)

func TestChromeInvocationArgumentsAreAcceptedButBounded(t *testing.T) {
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"
	for _, args := range [][]string{nil, {origin}, {origin, "--parent-window=0"}, {origin, "--parent-window=18446744073709551615"}} {
		if !validBrowserInvocationArgs(args) {
			t.Fatalf("valid arguments refused: %q", args)
		}
	}
	for _, args := range [][]string{{"https://page.example"}, {"chrome-extension://abc/"},
		{origin, "--parent-window=-1"}, {origin, "--parent-window=1", "extra"}} {
		if validBrowserInvocationArgs(args) {
			t.Fatalf("invalid arguments accepted: %q", args)
		}
	}
}

func TestFirefoxInvocationArgumentsAreAcceptedButBounded(t *testing.T) {
	manifest := "/Users/someone/Library/Application Support/Mozilla/NativeMessagingHosts/com.brique.browser.json"
	if firefoxExtensionID != launcher.FirefoxExtensionID {
		t.Fatalf("host and launcher disagree on the Firefox extension ID")
	}
	if !validBrowserInvocationArgs([]string{manifest, firefoxExtensionID}) {
		t.Fatal("Firefox invocation refused")
	}
	for _, args := range [][]string{
		{manifest}, {manifest, "other@example.org"}, {"relative/com.brique.browser.json", firefoxExtensionID},
		{"/tmp/other.json", firefoxExtensionID}, {manifest, firefoxExtensionID, "extra"}, {firefoxExtensionID, manifest},
	} {
		if validBrowserInvocationArgs(args) {
			t.Fatalf("invalid Firefox arguments accepted: %q", args)
		}
	}
}
