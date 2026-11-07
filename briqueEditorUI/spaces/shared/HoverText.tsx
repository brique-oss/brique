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


import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { popoverPlace } from "./hoverPosition.js";

const CLOSE_DELAY_MS = 120;

export type HoverTextProps = {
  /** The whole text, shown while the pointer is on the element (or on the popover). Nothing is shown when it is absent. */
  text: string | undefined;
  style?: React.CSSProperties;
  children: React.ReactNode;
};

/**
 * An element whose text may be shown in part: on hover, at once, the whole text opens next to the pointer. The popover is rendered
 * in the document body, so that no ancestor clips it, and it stays open while the pointer is on it, so a long text can be scrolled.
 */
export function HoverText({ text, style, children }: HoverTextProps) {
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  if (!text) return <span style={style}>{children}</span>;
  const keepOpen = () => window.clearTimeout(timer.current);
  const close = () => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setAt(null), CLOSE_DELAY_MS);
  };
  return (
    <>
      <span
        style={style}
        onMouseEnter={(e) => {
          keepOpen();
          const { clientX: x, clientY: y } = e;
          setAt((current) => current ?? { x, y });
        }}
        onMouseLeave={close}
      >
        {children}
      </span>
      {at &&
        createPortal(
          <div
            role="tooltip"
            style={{ ...styles.popover, ...popoverPlace(at, { width: window.innerWidth, height: window.innerHeight }) }}
            onMouseEnter={keepOpen}
            onMouseLeave={close}
          >
            {text}
          </div>,
          document.body
        )}
    </>
  );
}

const styles: Record<string, React.CSSProperties> = {
  popover: {
    position: "fixed",
    zIndex: 10000,
    maxWidth: 520,
    maxHeight: "60vh",
    overflow: "auto",
    padding: "8px 10px",
    boxSizing: "border-box",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: 8,
    background: "var(--syn-surface-overlay)",
    boxShadow: "0 12px 40px rgba(0,0,0,0.45)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
    fontSize: 11,
    lineHeight: 1.45,
    whiteSpace: "pre-wrap",
    overflowWrap: "anywhere",
  },
};
