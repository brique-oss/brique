// Pure model of the interface agentic: a journal (the lines of a continuity) becomes what the person sees (spec: document/agentic_interface.md).
// No React, no Pulse, no clock: everything here is a function of its arguments, so it is tested on real journals.

export type JournalLine = {
  id: string;
  seq: number;
  kind: string;
  from?: string;
  to?: string;
  content: any;
  correlation?: Record<string, any>;
  created_at?: string;
};

export type ViewItem =
  | { type: "user"; id: string; seq: number; text: string }
  | { type: "pilot"; id: string; seq: number; text: string; recipient: string | null }
  | { type: "daughter"; id: string; seq: number; from: string; text: string }
  | { type: "tool"; id: string; seq: number; capacity: string; context: string; status: "running" | "ok" | "error" | "sent"; parameters: any; result?: any; error?: string; mcp?: McpCall }
  | { type: "summary"; id: string; seq: number; text: string }
  | { type: "alert"; id: string; seq: number; event: string; text: string; messageId?: string; attempts?: number; parked?: boolean }
  | { type: "control"; id: string; seq: number; action: string }
  | InputItem;

/** A request for input (document/capacity_input_request.md): the question a pilot asked, and what became of it. Open until an answer or a closure carries its id. */
export type InputItem = {
  type: "input"; id: string; seq: number; question: string; kind: "text" | "choice" | "confirm"; choices: string[];
  status: "open" | "answered" | "closed"; answer?: string; closedReason?: string; expiresAt?: string;
};

/** A call of a pilot to an external MCP server, through the outbound gateway (/root/mcp/client/call), told apart from an ordinary Brique call.
 * state is the gateway's account: completed, remote_error, refused, failed_before_transmission, outcome_unknown. */
export type McpCall = { server: string; tool: string; state?: string; reason?: string; texts?: string };

export const MCP_GATEWAY_CALL = "/root/mcp/client/call";

/** deepwiki__read_wiki_structure -> server deepwiki, tool read_wiki_structure (the gateway's model-visible name; a shortened name keeps its hash). */
export function mcpNames(modelName: string): { server: string; tool: string } {
  const at = modelName.indexOf("__");
  return at < 0 ? { server: "", tool: modelName } : { server: modelName.slice(0, at), tool: modelName.slice(at + 2) };
}

const text = (value: any): string => (typeof value === "string" ? value : value === undefined || value === null ? "" : JSON.stringify(value));

const splitReference = (reference: string): { context: string; capacity: string } => {
  const at = reference.lastIndexOf("/");
  return at < 0 ? { context: "", capacity: reference } : { context: reference.slice(0, at) || "/", capacity: reference.slice(at + 1) };
};

/** The view of one journal. Lines that are the machinery's business (the frame, the model's own decisions, events) are not shown. */
export function toView(lines: JournalLine[]): ViewItem[] {
  const items: ViewItem[] = [];
  const tools = new Map<string, Extract<ViewItem, { type: "tool" }>>();
  const failures = new Map<string, Extract<ViewItem, { type: "alert" }>>();
  const inputs = new Map<string, InputItem>();

  for (const line of [...lines].sort((a, b) => a.seq - b.seq)) {
    const content = line.content;
    switch (line.kind) {
      case "user":
        items.push({ type: "user", id: line.id, seq: line.seq, text: text(content) });
        break;
      case "pilot": {
        const recipient = typeof line.to === "string" && !line.to.startsWith("user:") ? line.to : null;
        items.push({ type: "pilot", id: line.id, seq: line.seq, text: text(content?.content ?? content), recipient });
        break;
      }
      case "continuity": {
        const from = String(line.correlation?.continuity_id ?? String(line.from ?? "").replace(/^continuity:/, ""));
        items.push({ type: "daughter", id: line.id, seq: line.seq, from, text: text(content) });
        break;
      }
      case "capacity_intention": {
        const { context, capacity } = splitReference(String(content?.target_capacity ?? ""));
        const card: Extract<ViewItem, { type: "tool" }> = {
          type: "tool", id: String(content?.id ?? line.id), seq: line.seq, capacity, context,
          status: content?.await_response === false ? "sent" : "running", parameters: content?.parameters ?? {},
        };
        if (content?.target_capacity === MCP_GATEWAY_CALL) card.mcp = mcpNames(String(content?.parameters?.tool ?? ""));
        tools.set(card.id, card);
        items.push(card);
        break;
      }
      case "capacity_response": {
        const card = tools.get(String(line.correlation?.intention_id ?? ""));
        const response = content?.response ?? {};
        if (card) {
          card.status = response.status === "error" ? "error" : "ok";
          if (response.status === "error") card.error = text(response.error?.message ?? response.error);
          else card.result = response.payload;
          if (card.mcp) {
            // the gateway answers ok with the state of the call: only a completed call is a success
            const payload = response.payload ?? {};
            card.mcp.state = response.status === "error" || content?.unresolved ? "outcome_unknown" : String(payload.state ?? "");
            card.mcp.reason = payload.reason ? String(payload.reason) : undefined;
            card.mcp.texts = (payload.result?.content ?? []).filter((b: any) => b?.type === "text").map((b: any) => String(b.text ?? "")).join("\n");
            if (card.mcp.state !== "completed") card.status = "error";
          }
        }
        break;
      }
      case "summary":
        items.push({ type: "summary", id: line.id, seq: line.seq, text: text(content) });
        break;
      case "error": {
        const event = String(content?.event ?? "error");
        const messageId = content?.message_id ? String(content.message_id) : undefined;
        if ((event === "treatment_failed" || event === "message_parked") && messageId) {
          // one message that fails three times and is set aside is one alert, not four lines
          let alert = failures.get(messageId);
          if (!alert) {
            alert = { type: "alert", id: line.id, seq: line.seq, event: "treatment_failed", text: "", messageId, attempts: 0, parked: false };
            failures.set(messageId, alert);
            items.push(alert);
          }
          if (event === "treatment_failed") { alert.attempts = (alert.attempts ?? 0) + 1; alert.text = text(content?.error); }
          else alert.parked = true;
        } else {
          items.push({ type: "alert", id: line.id, seq: line.seq, event, text: text(content?.error ?? content?.errors ?? content), messageId });
        }
        break;
      }
      case "control":
        items.push({ type: "control", id: line.id, seq: line.seq, action: String(content?.action ?? text(content)) });
        break;
      case "event": {
        const requestId = typeof content?.request_id === "string" ? content.request_id : null;
        if (!requestId) break;                                   // the other facts of follow-up are the machinery's business
        if (content.event === "input_request" && !inputs.has(requestId)) {
          const kind = content.answer_kind === "choice" || content.answer_kind === "confirm" ? content.answer_kind : "text";
          const card: InputItem = { type: "input", id: requestId, seq: line.seq, question: text(content.question), kind,
            choices: Array.isArray(content.choices) ? content.choices.map(String) : [], status: "open", expiresAt: content.expires_at };
          inputs.set(requestId, card);
          items.push(card);
        } else if (content.event === "input_response" || content.event === "input_closed") {
          const card = inputs.get(requestId);
          if (card && card.status === "open") {                  // the first in the journal wins
            if (content.event === "input_response") { card.status = "answered"; card.answer = text(content.answer); }
            else { card.status = "closed"; card.closedReason = String(content.reason ?? "closed"); }
          }
        }
        break;
      }
      default:
        break; // framework, prompt, completion, projection
    }
  }
  return items;
}

/** The view of a whole lineage: its generations in order. A user request handed over by a succession is in two journals: it is shown where it
 *  was first written, and the summary of each generation stands where the conversation continued from it. */
export function lineageView(generations: Array<{ id: string; lines: JournalLine[] }>): ViewItem[] {
  const seen = new Set<string>();
  const out: ViewItem[] = [];
  for (const generation of generations) {
    for (const item of toView(generation.lines)) {
      if (item.type === "user") {
        if (seen.has(item.id)) continue;
        seen.add(item.id);
      }
      out.push(item);
    }
  }
  return out;
}
