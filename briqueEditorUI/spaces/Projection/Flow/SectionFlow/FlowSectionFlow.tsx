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

import { useLayoutEffect, useMemo, useRef, useState } from "react";
import type { FlowProjectionModel } from "../../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import type { FlowCapacityTarget } from "../../contracts.js";
import type { FlowExpansion } from "./contracts.js";
import { FlowLayoutRenderer, type FlowBriqueRefCallbacks } from "./FlowLayoutRenderer.js";
import { buildCapacityLayout } from "./flowLayoutBuilder.js";

export type FlowSectionFlowProps = {
  model: FlowProjectionModel | undefined;
  onFlowCapacityTargetChange: (target: FlowCapacityTarget) => void;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
  onRefresh?: () => void;
};

export function FlowSectionFlow({
  model,
  onFlowCapacityTargetChange,
  briqueRefCallbacks,
  onRefresh,
}: FlowSectionFlowProps) {
  const [sectionExpansions, setSectionExpansions] = useState<Record<string, FlowExpansion>>({});
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [scale, setScale] = useState(1);
  const [panning, setPanning] = useState(false);
  const dragRef = useRef<{ startX: number; startY: number; originX: number; originY: number } | null>(null);
  const viewportRef = useRef<HTMLElement>(null);
  const surfaceRef = useRef<HTMLDivElement>(null);

  // Center layout on mount (component is remounted on capacity change via key prop)
  useLayoutEffect(() => {
    const raf = requestAnimationFrame(() => {
      const viewport = viewportRef.current;
      const surface = surfaceRef.current;
      if (!viewport || !surface) return;
      const vw = viewport.clientWidth;
      const sw = surface.offsetWidth;
      if (sw === 0) return;
      setOffset({ x: Math.max(0, (vw - sw) / 2), y: 20 });
    });
    return () => cancelAnimationFrame(raf);
  }, []);

  const layout = useMemo(() => {
    if (!model) return undefined;
    return buildCapacityLayout(model, sectionExpansions);
  }, [model, sectionExpansions]);

  return (
    <section
      ref={viewportRef}
      aria-label="Flow section flow"
      data-component="flow-section-flow"
      onPointerCancel={() => {
        dragRef.current = null;
        setPanning(false);
      }}
      onPointerDown={(event) => {
        if (event.button !== 0) return;
        if (isInteractiveTarget(event.target)) return;
        event.currentTarget.setPointerCapture(event.pointerId);
        dragRef.current = {
          startX: event.clientX,
          startY: event.clientY,
          originX: offset.x,
          originY: offset.y,
        };
        setPanning(true);
      }}
      onPointerMove={(event) => {
        const drag = dragRef.current;
        if (!drag) return;
        setOffset({
          x: drag.originX + (event.clientX - drag.startX),
          y: drag.originY + (event.clientY - drag.startY),
        });
      }}
      onPointerUp={(event) => {
        event.currentTarget.releasePointerCapture(event.pointerId);
        dragRef.current = null;
        setPanning(false);
      }}
      onWheel={(event) => {
        event.preventDefault();
        if (event.ctrlKey) {
          const factor = event.deltaY > 0 ? 0.9 : 1.1;
          const nextScale = clamp(scale * factor, 0.25, 4);
          if (nextScale === scale) return;

          const rect = event.currentTarget.getBoundingClientRect();
          const pointerX = event.clientX - rect.left;
          const pointerY = event.clientY - rect.top;
          const contentX = (pointerX - offset.x) / scale;
          const contentY = (pointerY - offset.y) / scale;

          setScale(nextScale);
          setOffset({
            x: pointerX - contentX * nextScale,
            y: pointerY - contentY * nextScale,
          });
        } else {
          setOffset((current) => ({
            x: current.x - event.deltaX,
            y: current.y - event.deltaY,
          }));
        }
      }}
      style={{
        ...styles.root,
        cursor: panning ? "grabbing" : "grab",
        userSelect: panning ? "none" : "text",
      }}
    >
      {layout && (
        <div
          ref={surfaceRef}
          style={{
            position: "absolute",
            top: 0,
            left: 0,
            transformOrigin: "0 0",
            transform: `translate(${offset.x}px, ${offset.y}px) scale(${scale})`,
          }}
        >
          <FlowLayoutRenderer
            layout={layout}
            sectionExpansions={sectionExpansions}
            onFlowCapacityTargetChange={onFlowCapacityTargetChange}
            onSectionExpansionChange={(nodeId, expansion) =>
              setSectionExpansions((current) => ({ ...current, [nodeId]: expansion }))
            }
            briqueRefCallbacks={briqueRefCallbacks}
            onRefresh={onRefresh}
          />
        </div>
      )}
    </section>
  );
}

function isInteractiveTarget(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(
    target.closest("button,a,input,textarea,select,[role='button'],[data-brique-ref],[data-flow-leaf]")
  );
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    flex: "1 1 0%",
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
    background: "var(--syn-flow-panel-bg)",
    position: "relative",
  },
};

const globalStyle = document.createElement("style");
globalStyle.textContent = `
  [data-component="flow-section-flow"]::-webkit-scrollbar {
    display: none;
  }
`;
document.head.appendChild(globalStyle);
