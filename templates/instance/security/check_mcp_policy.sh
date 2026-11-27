#!/bin/sh
# Policy matrix of the MCP realization (root/mcp/document/mcp_realization_plan.md, WP3).
# Checks with `brique-sec simulate` that the access source gives each principal exactly the access the plan
# expects on the protected MCP gateway and elsewhere. Read-only: it reads the source, it changes nothing.
#
#   BRIQUE_SEC=/path/to/brique-sec security/check_mcp_policy.sh
#
# A wrapper with no principal is not in this matrix: `simulate` knows only principals. Such a wrapper is
# refused on the protected capacities by the engine's protected gate (engine/SECURITY.md §3.10), which the
# engine tests prove.
set -u
SEC=${BRIQUE_SEC:-brique-sec}
ROOT=$(cd "$(dirname "$0")/.." && pwd)/root
failures=0

expect() { # principal verb target ALLOWED|DENIED
	got=$("$SEC" --root "$ROOT" simulate "$1" "$2" "$3" 2>&1 | head -1 | cut -d: -f1)
	if [ "$got" = "$4" ]; then
		echo "ok    $1 $2 $3 -> $4"
	else
		echo "FAIL  $1 $2 $3 -> $got, want $4"
		failures=$((failures + 1))
	fi
}

for capacity in call tools status; do
	expect local.agentic_core call "/root/mcp/client/capacity/$capacity" ALLOWED
	expect local.mcp_reader call "/root/mcp/client/capacity/$capacity" DENIED
	expect local.ui call "/root/mcp/client/capacity/$capacity" DENIED
done
expect local.agentic_core call "/root/llm/capacity/infer" ALLOWED
expect local.agentic_core call "/root/agentic/capacity/mailbox.send" ALLOWED
expect local.mcp_reader call "/root/llm/capacity/infer" ALLOWED
expect local.mcp_remote call "/root/mcp/client/capacity/call" DENIED
expect local.mcp_remote call "/root/mcp/client/capacity/status" DENIED
expect local.mcp_remote call "/root/llm/capacity/infer" ALLOWED
expect local.mcp_remote write "/root/agentic/matter/x" DENIED
expect local.mcp_reader call "/root/mcp/capacity/brique.intention" ALLOWED
expect local.ui call "/root/agentic/capacity/pilot.activate" ALLOWED
expect local.ui define "/root/mcp/client/capacity/call" DENIED
expect local.ui define "/root/agentic/capacity/pilot.activate" ALLOWED
expect local.ui control "/root/mcp/client" DENIED
expect local.ui control "/root/agentic" ALLOWED
expect local.ui write "/root/mcp/client/matter/x" DENIED
expect local.ui read "/root/mcp/client/capacity/call" ALLOWED
expect local.agentic_core write "/root/agentic/matter/x" ALLOWED
expect local.agentic_core define "/root/agentic/capacity/x" DENIED
# letter case never changes a decision
expect local.mcp_reader call "/ROOT/MCP/Client/capacity/CALL" DENIED
expect local.ui write "/root/mcp/client/matter/Commande_CMD-1042" DENIED
expect local.agentic_core write "/root/agentic/matter/Commande_CMD-1042" ALLOWED

if [ "$failures" -ne 0 ]; then
	echo "$failures failure(s)"
	exit 1
fi
echo "policy matrix: all as expected"
