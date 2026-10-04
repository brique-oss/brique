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

import { useCallback, useEffect, useRef, useState } from "react";
import type {
  ActiveContext,
  ActiveContextActivationRequest,
  HierarchyDisplayedResponseState,
} from "./contracts.js";
import { StructureContextMap } from "./StructureContextMap.js";

export type ContextMapFloatingWindowProps = {
  activeContext: ActiveContext;
  hierarchyState: HierarchyDisplayedResponseState;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onClose: () => void;
  onOpenInNewTab?: (contextPath: string) => void;
};

type Position = { x: number; y: number };
type Size = { width: number; height: number };

const INITIAL_WIDTH = 320;
const INITIAL_HEIGHT = 360;
const INITIAL_X = 0;
const INITIAL_Y_RATIO = 0.05;
const MIN_WIDTH = 200;
const MIN_HEIGHT = 160;
const HEADER_HEIGHT = 32;

export function ContextMapFloatingWindow({
  activeContext,
  hierarchyState,
  onActivateContext,
  onClose,
  onOpenInNewTab,
}: ContextMapFloatingWindowProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<Position | null>(null);
  const [size, setSize] = useState<Size>({ width: INITIAL_WIDTH, height: INITIAL_HEIGHT });

  useEffect(() => {
    const parentHeight = containerRef.current?.parentElement?.getBoundingClientRect().height ?? 0;
    setPos({ x: INITIAL_X, y: parentHeight * INITIAL_Y_RATIO });
  }, []);

  // --- drag ---
  const dragStart = useRef<{ mx: number; my: number; ox: number; oy: number } | null>(null);

  const onHeaderMouseDown = useCallback((e: React.MouseEvent) => {
    if ((e.target as HTMLElement).closest("button")) return;
    e.preventDefault();
    dragStart.current = {
      mx: e.clientX,
      my: e.clientY,
      ox: pos?.x ?? INITIAL_X,
      oy: pos?.y ?? 0,
    };

    function onMouseMove(ev: MouseEvent) {
      if (!dragStart.current || !containerRef.current) return;
      const parent = containerRef.current.parentElement;
      if (!parent) return;
      const { width: pw, height: ph } = parent.getBoundingClientRect();
      const dx = ev.clientX - dragStart.current.mx;
      const dy = ev.clientY - dragStart.current.my;
      const nx = Math.max(0, Math.min(pw - size.width, dragStart.current.ox + dx));
      const ny = Math.max(0, Math.min(ph - HEADER_HEIGHT, dragStart.current.oy + dy));
      setPos({ x: nx, y: ny });
    }

    function onMouseUp() {
      dragStart.current = null;
      window.removeEventListener("mousemove", onMouseMove);
      window.removeEventListener("mouseup", onMouseUp);
    }

    window.addEventListener("mousemove", onMouseMove);
    window.addEventListener("mouseup", onMouseUp);
  }, [pos, size.width]);

  // --- resize ---
  const resizeStart = useRef<{
    mx: number; my: number;
    ow: number; oh: number;
    ox: number; oy: number;
    dir: string;
  } | null>(null);

  const onResizeMouseDown = useCallback((e: React.MouseEvent, dir: string) => {
    e.preventDefault();
    e.stopPropagation();
    resizeStart.current = {
      mx: e.clientX,
      my: e.clientY,
      ow: size.width,
      oh: size.height,
      ox: pos?.x ?? INITIAL_X,
      oy: pos?.y ?? 0,
      dir,
    };

    function onMouseMove(ev: MouseEvent) {
      if (!resizeStart.current || !containerRef.current) return;
      const parent = containerRef.current.parentElement;
      if (!parent) return;
      const { width: pw, height: ph } = parent.getBoundingClientRect();
      const dx = ev.clientX - resizeStart.current.mx;
      const dy = ev.clientY - resizeStart.current.my;
      const { ow, oh, ox, oy, dir: d } = resizeStart.current;

      let nw = ow, nh = oh, nx = ox, ny = oy;

      if (d.includes("e")) nw = Math.max(MIN_WIDTH, Math.min(pw - ox, ow + dx));
      if (d.includes("s")) nh = Math.max(MIN_HEIGHT, Math.min(ph - oy, oh + dy));
      if (d.includes("w")) {
        nw = Math.max(MIN_WIDTH, ow - dx);
        nx = Math.max(0, Math.min(ox + dx, ox + ow - MIN_WIDTH));
      }
      if (d.includes("n")) {
        nh = Math.max(MIN_HEIGHT, oh - dy);
        ny = Math.max(0, Math.min(oy + dy, oy + oh - MIN_HEIGHT));
      }

      setSize({ width: nw, height: nh });
      setPos({ x: nx, y: ny });
    }

    function onMouseUp() {
      resizeStart.current = null;
      window.removeEventListener("mousemove", onMouseMove);
      window.removeEventListener("mouseup", onMouseUp);
    }

    window.addEventListener("mousemove", onMouseMove);
    window.addEventListener("mouseup", onMouseUp);
  }, [size, pos]);

  if (pos === null) return <div ref={containerRef} style={{ position: "absolute" }} />;

  return (
    <div
      ref={containerRef}
      style={{
        position: "absolute",
        left: pos.x,
        top: pos.y,
        width: size.width,
        height: size.height,
        zIndex: 100,
        display: "flex",
        flexDirection: "column",
        borderRadius: "8px",
        overflow: "hidden",
        boxShadow: "0 4px 24px rgba(0,0,0,0.5)",
        border: "1px solid var(--syn-border-medium)",
      }}
    >
      <div style={styles.header} onMouseDown={onHeaderMouseDown}>
        <span style={styles.headerTitle}>Context Map</span>
        <button style={styles.closeBtn} onClick={onClose}>✕</button>
      </div>

      <div style={{ flex: 1, minHeight: 0, overflow: "hidden" }}>
        <StructureContextMap
          activeContext={activeContext}
          hierarchyState={hierarchyState}
          onActivateContext={onActivateContext}
          onOpenInNewTab={onOpenInNewTab}
        />
      </div>

      <div style={{ ...styles.resizeHandle, ...styles.resizeE }} onMouseDown={(e) => onResizeMouseDown(e, "e")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeS }} onMouseDown={(e) => onResizeMouseDown(e, "s")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeW }} onMouseDown={(e) => onResizeMouseDown(e, "w")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeN }} onMouseDown={(e) => onResizeMouseDown(e, "n")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeSE }} onMouseDown={(e) => onResizeMouseDown(e, "se")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeSW }} onMouseDown={(e) => onResizeMouseDown(e, "sw")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeNE }} onMouseDown={(e) => onResizeMouseDown(e, "ne")} />
      <div style={{ ...styles.resizeHandle, ...styles.resizeNW }} onMouseDown={(e) => onResizeMouseDown(e, "nw")} />
    </div>
  );
}

const HANDLE = 6;
const CORNER = 10;

const styles: Record<string, React.CSSProperties> = {
  header: {
    height: HEADER_HEIGHT,
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "0 8px 0 12px",
    background: "var(--syn-structure-panel-bg)",
    cursor: "grab",
    userSelect: "none",
    flexShrink: 0,
    borderBottom: "1px solid var(--syn-border-soft)",
  },
  headerTitle: {
    fontSize: "11px",
    fontWeight: 700,
    opacity: 0.7,
    textTransform: "uppercase",
    letterSpacing: "0.05em",
  },
  closeBtn: {
    background: "none",
    border: "none",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "12px",
    opacity: 0.6,
    padding: "2px 4px",
  },
  resizeHandle: {
    position: "absolute",
    zIndex: 10,
  },
  resizeE: { right: 0, top: CORNER, bottom: CORNER, width: HANDLE, cursor: "ew-resize" },
  resizeW: { left: 0, top: CORNER, bottom: CORNER, width: HANDLE, cursor: "ew-resize" },
  resizeS: { bottom: 0, left: CORNER, right: CORNER, height: HANDLE, cursor: "ns-resize" },
  resizeN: { top: 0, left: CORNER, right: CORNER, height: HANDLE, cursor: "ns-resize" },
  resizeSE: { right: 0, bottom: 0, width: CORNER, height: CORNER, cursor: "se-resize" },
  resizeSW: { left: 0, bottom: 0, width: CORNER, height: CORNER, cursor: "sw-resize" },
  resizeNE: { right: 0, top: 0, width: CORNER, height: CORNER, cursor: "ne-resize" },
  resizeNW: { left: 0, top: 0, width: CORNER, height: CORNER, cursor: "nw-resize" },
};
