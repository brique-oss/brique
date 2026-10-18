package wrapper

import (
	"strings"
	"testing"
)

func TestLocalMarshalControlMessageRejectsBulkPayload(t *testing.T) {
	if _, err := marshalControlMessage(map[string]any{"payload": "small"}); err != nil {
		t.Fatalf("small control message rejected: %v", err)
	}
	if _, err := marshalControlMessage(map[string]any{"payload": strings.Repeat("x", ControlMessageLimitBytes)}); err == nil || !strings.Contains(err.Error(), "Matter substance") {
		t.Fatalf("oversized control message should direct caller to Matter, err=%v", err)
	}
}
