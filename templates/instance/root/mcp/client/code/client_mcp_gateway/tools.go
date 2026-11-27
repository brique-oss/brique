// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"brique/wrapper/wrapper"
	"client_mcp_gateway/gateway"
)

// <brique:capacity name="tools">
// Contract: /root/mcp/client/capacity/tools.json.
func tools(gw *gateway.Gateway) func(*wrapper.Params) (any, error) {
	return func(p *wrapper.Params) (any, error) {
		servers, ok := stringList(p, "servers")
		if !ok {
			return nil, invalidParam("servers must be a list of server aliases")
		}
		mandatory, ok := stringList(p, "mandatory")
		if !ok {
			return nil, invalidParam("mandatory must be a list of server aliases")
		}
		stop := p.Keepalive()
		defer stop()
		return answer(gw.Tools(p.Context(), gateway.ToolsRequest{Servers: servers, Mandatory: mandatory}))
	}
}

// </brique:capacity>
