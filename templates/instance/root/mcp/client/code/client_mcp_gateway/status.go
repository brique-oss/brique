// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"brique/wrapper/wrapper"
	"client_mcp_gateway/gateway"
)

// <brique:capacity name="status">
// Contract: /root/mcp/client/capacity/status.json.
func status(gw *gateway.Gateway) func(*wrapper.Params) (any, error) {
	return func(p *wrapper.Params) (any, error) {
		return answer(gw.Status(p.String("call_id")))
	}
}

// </brique:capacity>
