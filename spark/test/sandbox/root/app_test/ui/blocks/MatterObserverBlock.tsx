/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import React, { useRef } from "react";
import { useMatter, usePulse } from "@spark/pulse/react";

export type MatterObserverBlockProps = {
  context: string;
  matterId: string;
  label: string;
  testId: string;
  subscribe?: boolean;
};

function MatterObserverBlockView({
  context,
  matterId,
  label,
  testId,
  subscribe = false,
}: MatterObserverBlockProps) {
  const pulse = usePulse();
  const renderCount = useRef(0);
  renderCount.current += 1;
  const matter = useMatter(
    {
      context,
      matterId,
    },
    {
      refetchOnEvent: true,
      subscribe,
    }
  );
  const key = {
    context,
    matterId,
    readMode: null,
  };
  const actualReads = pulse.matter.getReadRequestCount(key);
  const pendingRead = pulse.matter.hasPendingRead(key);

  return (
    <div data-testid={testId} style={styles.box}>
      <strong>{label}</strong>
      <div data-testid={`${testId}-value`}>value: {formatMatterValue(matter.data)}</div>
      <div data-testid={`${testId}-status`}>status: {matter.status}</div>
      <div data-testid={`${testId}-last-event`}>
        lastEvent: {matter.lastEvent?.event ?? "none"}
      </div>
      <div data-testid={`${testId}-mode`}>
        mode: {subscribe ? "subscribe" : "read"}
      </div>
      <div data-testid={`${testId}-actual-reads`}>
        actualReads: {actualReads}
      </div>
      <div data-testid={`${testId}-pending-read`}>
        pendingRead: {String(pendingRead)}
      </div>
      <div data-testid={`${testId}-readers`}>
        readers: {pulse.matter.getReaderCount(key)}
      </div>
      <div data-testid={`${testId}-subscribers`}>
        subscribers: {pulse.matter.getSubscriberCount(key)}
      </div>
      <div data-testid={`${testId}-sub-id`}>subId: {matter.subId ?? "none"}</div>
      <div data-testid={`${testId}-render-count`} hidden>
        renderCount: {renderCount.current}
      </div>
    </div>
  );
}

function formatMatterValue(value: unknown): string {
  if (value === null || value === undefined) {
    return "none";
  }
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  const inlineValue = decodeInlineValue(value);
  if (inlineValue !== null) {
    return formatMatterValue(inlineValue);
  }
  if (isValueObject(value)) {
    return formatMatterValue(value.value);
  }

  return JSON.stringify(value);
}

function isValueObject(value: unknown): value is { value: unknown } {
  return typeof value === "object" && value !== null && "value" in value;
}

function decodeInlineValue(value: unknown): unknown | null {
  if (
    typeof value !== "object" ||
    value === null ||
    !("kind" in value) ||
    !("bytes" in value)
  ) {
    return null;
  }

  const inline = value as { kind?: unknown; bytes?: unknown };
  if (inline.kind !== "inline" || typeof inline.bytes !== "string") {
    return null;
  }

  try {
    const binary = atob(inline.bytes);
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
    const text = new TextDecoder().decode(bytes);
    return JSON.parse(text);
  } catch {
    return null;
  }
}

const styles: Record<string, React.CSSProperties> = {
  box: {
    border: "1px dashed #8c959f",
    borderRadius: 6,
    padding: 8,
    fontSize: 12,
    background: "rgba(255,255,255,0.7)",
  },
};

export const MatterObserverBlock = React.memo(MatterObserverBlockView);
MatterObserverBlock.displayName = "MatterObserverBlock";
