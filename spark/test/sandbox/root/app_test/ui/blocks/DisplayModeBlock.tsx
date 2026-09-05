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
import { useDisplayMode, useSetDisplayMode } from "@spark/pulse/react";
import type { DisplayMode } from "@spark/pulse/runtime";

const modes: DisplayMode[] = ["editor", "preview", "debug"];

export function DisplayModeBlock() {
  const mode = useDisplayMode();
  const setDisplayMode = useSetDisplayMode();

  return (
    <div style={styles.group}>
      {modes.map((candidate) => (
        <button
          key={candidate}
          data-testid={`display-mode-${candidate}`}
          aria-pressed={mode === candidate}
          onClick={() => setDisplayMode(candidate)}
          style={mode === candidate ? styles.active : styles.button}
        >
          {candidate}
        </button>
      ))}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  group: {
    display: "flex",
    flexWrap: "wrap",
    gap: 6,
  },
  button: {
    minHeight: 32,
  },
  active: {
    minHeight: 32,
    fontWeight: 700,
    borderColor: "#0969da",
  },
};
