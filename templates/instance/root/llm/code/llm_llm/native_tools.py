"""Native tool use in the canonical LLM contract (schema/request.json, schema/response.json).

Substrate shared by the provider adapters, no capacity of its own. A request uses native tools when it
declares `tools`, or when its history holds tool blocks (a compaction may send that history with
tool_choice none). Canonical blocks of a message whose content is a list:

    {"type": "text", "text": "..."}
    {"type": "tool_call", "id": "...", "name": "...", "arguments": {...}}          assistant
    {"type": "tool_result", "call_id": "...", "content": "...", "is_error": false}  user
    {"type": "provider_block", "provider": "anthropic", "block": {...}}             assistant, returned unchanged

A provider is used with native tools only once it is qualified (root/mcp/document/provider_qualification.md);
the others refuse such a request explicitly instead of dropping the tools.
"""
from __future__ import annotations

from wrapper.calls import CapacityCallError

TOOL_BLOCKS = ("tool_call", "tool_result", "provider_block")


def uses_native_tools(request: dict) -> bool:
    if request.get("tools"):
        return True
    for message in request.get("messages") or []:
        content = message.get("content")
        if isinstance(content, list) and any(isinstance(b, dict) and b.get("type") in TOOL_BLOCKS for b in content):
            return True
    return False


def refuse_native_tools(request: dict, provider: str) -> None:
    """The adapters of providers that are not qualified call this first."""
    if uses_native_tools(request):
        raise CapacityCallError("invalid", f"provider '{provider}' is not qualified for native tools",
                                {"reason": "tools_not_supported", "provider": provider})


def check_tool_pairing(messages: list) -> None:
    """Every tool_call of an assistant message is answered once, by the user messages that follow it and
    before the next assistant message, with tool results placed before any other block of their message;
    no result answers an unknown call. A provider refuses anything else (H3, H4, D3, D4 of the qualification)."""
    pending: set[str] = set()
    for index, message in enumerate(messages):
        role = message.get("role")
        content = message.get("content")
        blocks = content if isinstance(content, list) else []
        if role == "assistant":
            if pending:
                raise ValueError(f"message {index}: tool calls {sorted(pending)} have no result before the next assistant turn")
            ids = [b.get("id") for b in blocks if b.get("type") == "tool_call"]
            if len(set(ids)) != len(ids) or any(not i for i in ids):
                raise ValueError(f"message {index}: tool call ids must be present and distinct")
            pending = set(ids)
        elif role == "user":
            seen_other = False
            for block in blocks:
                if block.get("type") == "tool_result":
                    if seen_other:
                        raise ValueError(f"message {index}: a tool result must come before any other block")
                    call_id = block.get("call_id")
                    if call_id not in pending:
                        raise ValueError(f"message {index}: tool result for unknown or already answered call {call_id!r}")
                    pending.discard(call_id)
                else:
                    seen_other = True
            if pending and not any(b.get("type") == "tool_result" for b in blocks):
                raise ValueError(f"message {index}: tool calls {sorted(pending)} are not answered immediately")
        elif any(b.get("type") in TOOL_BLOCKS for b in blocks):
            raise ValueError(f"message {index}: tool blocks belong to assistant and user messages")
    if pending:
        raise ValueError(f"tool calls {sorted(pending)} have no result")


def validate_tools(tools: list) -> None:
    if not isinstance(tools, list):
        raise ValueError("request.tools must be a list")
    names = set()
    for tool in tools:
        name = tool.get("name") if isinstance(tool, dict) else None
        if not isinstance(name, str) or not name or name in names:
            raise ValueError("every tool has a distinct name")
        if not isinstance(tool.get("input_schema"), dict):
            raise ValueError(f"tool {name!r} has no input_schema object")
        names.add(name)
