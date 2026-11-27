// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package main

import (
	"brique/wrapper/wrapper"
	"client_mcp_gateway/gateway"
)

// <brique:capacity name="call">
// Contract: /root/mcp/client/capacity/call.json. The caller's address is the origin the engine set on the
// intention (Params.Caller), never a parameter.
func call(gw *gateway.Gateway) func(*wrapper.Params) (any, error) {
	return func(p *wrapper.Params) (any, error) {
		arguments, ok := objectParam(p, "arguments")
		if !ok {
			return nil, invalidParam("arguments must be an object")
		}
		caller := p.Caller()
		req := gateway.CallRequest{
			CallID: p.String("call_id"), Origin: p.String("origin"), Envelope: p.String("envelope"),
			ProjectionID: p.String("projection_id"), Tool: p.String("tool"), ArgumentsJSON: p.String("arguments_json"),
			Server: p.String("server"), Arguments: arguments, CallerContext: caller.Context, CallerCapacity: caller.Cap,
		}
		stop := p.Keepalive()
		defer stop()
		return answer(gw.Call(p.Context(), req))
	}
}

// </brique:capacity>
