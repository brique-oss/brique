// The requests for input of a conversation: what a pilot asked the person, and the window where the person answers
// (document/capacity_input_request.md). The texts below are PROVISIONAL, to be validated by the architect.
import React, { useState } from "react";
import type { InputItem } from "./model/journal.ts";
import { closedText } from "./model/input.ts";
import { styles } from "./styles.ts";

export const INPUT_LABELS = {
  waiting: "Your response is required",
  answer: "Answer",
  yes: "Yes",
  no: "No",
  placeholder: "Your answer…",
  answered: "Answer",
  timeout: "The request is closed (timed out)",
  cancelled: "The request was cancelled",
  closed: "The request is closed",
  row: "waiting for your answer",
} as const;

const L = INPUT_LABELS;

/** What the stream says of a request: its question, and what became of it (an open one is answered in the window under the stream). */
export function InputCard({ item }: { item: InputItem }) {
  return (
    <div style={styles.inputCard}>
      <div style={styles.bubbleLabel}>{item.status === "open" ? L.waiting : item.status === "answered" ? L.answered : closedText(item, L)}</div>
      <div>{item.question}</div>
      {item.status === "answered" && <div style={{ opacity: 0.8 }}>{L.answered} : {item.answer}</div>}
    </div>
  );
}

/** The window of the requests waiting for the person, the oldest first. A refused answer is shown here and the request stays: the person can correct. */
export function InputPanel({ requests, onAnswer }: { requests: InputItem[]; onAnswer: (request: InputItem, answer: string) => Promise<void> }) {
  if (requests.length === 0) return null;
  return (
    <div style={styles.inputPanel} role="dialog" aria-label={L.waiting}>
      <div style={styles.title}>{L.waiting}</div>
      {requests.map((request) => <InputForm key={request.id} request={request} onAnswer={onAnswer} />)}
    </div>
  );
}

function InputForm({ request, onAnswer }: { request: InputItem; onAnswer: (request: InputItem, answer: string) => Promise<void> }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const send = async (answer: string) => {
    setBusy(true); setError(null);
    try { await onAnswer(request, answer); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
  };
  const disabled = busy ? styles.buttonDisabled : undefined;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      <div>{request.question}</div>
      {request.kind === "text" && (
        <div style={{ display: "flex", gap: 8 }}>
          <textarea style={{ ...styles.input, minHeight: 40 }} value={text} placeholder={L.placeholder} disabled={busy} onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); if (text.trim()) void send(text); } }} />
          <button style={{ ...styles.button, ...disabled }} disabled={busy || !text.trim()} onClick={() => void send(text)}>{L.answer}</button>
        </div>
      )}
      {request.kind === "choice" && (
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          {request.choices.map((choice) => <button key={choice} style={{ ...styles.button, ...disabled }} disabled={busy} onClick={() => void send(choice)}>{choice}</button>)}
        </div>
      )}
      {request.kind === "confirm" && (
        <div style={{ display: "flex", gap: 8 }}>
          <button style={{ ...styles.button, ...disabled }} disabled={busy} onClick={() => void send("yes")}>{L.yes}</button>
          <button style={{ ...styles.buttonQuiet, ...disabled }} disabled={busy} onClick={() => void send("no")}>{L.no}</button>
        </div>
      )}
      {error && <div style={styles.error}>{error}</div>}
    </div>
  );
}
