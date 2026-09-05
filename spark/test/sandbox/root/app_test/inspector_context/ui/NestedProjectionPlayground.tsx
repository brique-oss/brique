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

import React from "react";
import { usePulse } from "@spark/pulse/react";
import { PathRendererBlock } from "../../ui/blocks/PathRendererBlock.js";
import {
  childContext,
  describeSandboxEmission,
  emitSandboxIntention,
  type SandboxEmission,
  type SandboxTarget,
} from "../../ui/blocks/sandboxPulse.js";
import { sandboxUIPaths } from "../../ui/blocks/sandboxPaths.js";

export function NestedProjectionPlayground({
  rootContextId,
  onIntention,
}: {
  rootContextId?: string;
  onIntention: (emission: SandboxEmission) => void;
}) {
  return (
    <section style={styles.section}>
      <h2 style={styles.heading}>Nested</h2>
      <ParentProjectionBlock rootContextId={rootContextId} onIntention={onIntention} />
    </section>
  );
}

function ParentProjectionBlock({
  rootContextId,
  onIntention,
}: {
  rootContextId?: string;
  onIntention: (emission: SandboxEmission) => void;
}) {
  return (
    <div style={styles.parent}>
      <ChildProjectionBlock rootContextId={rootContextId} onIntention={onIntention} />
    </div>
  );
}

function ChildProjectionBlock({
  rootContextId,
  onIntention,
}: {
  rootContextId?: string;
  onIntention: (emission: SandboxEmission) => void;
}) {
  const pulse = usePulse();
  const inspectorContext = childContext(rootContextId, "inspector_context");

  async function emitNested() {
    const target: SandboxTarget = {
      context: inspectorContext,
      cap: "echo",
      params: { message: "nested" },
      targetPathUI: sandboxUIPaths.nestedResult,
    };
    const id = await emitSandboxIntention(pulse, target);
    onIntention(describeSandboxEmission(pulse, target, id));
  }

  return (
    <div style={styles.child}>
      <button data-testid="nested-echo-button" onClick={emitNested}>Nested Echo</button>
      <PathRendererBlock pathUI={sandboxUIPaths.nestedResult} testId="nested-result-renderer" />
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  section: {
    border: "1px solid #d0d7de",
    borderRadius: 8,
    padding: 14,
    background: "#eaf7ee",
  },
  heading: {
    fontSize: 16,
    margin: "0 0 10px",
  },
  parent: {
    border: "1px solid #b7c4c0",
    borderRadius: 6,
    padding: 10,
  },
  child: {
    display: "grid",
    gap: 10,
  },
};
