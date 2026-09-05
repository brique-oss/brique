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

import { useState } from "react";
import { useThemeController } from "./ThemeProvider.js";

export function ThemeConfigurationButton() {
  const { openConfigurator } = useThemeController();
  const [pressed, setPressed] = useState(false);
  return (
    <button
      type="button"
      aria-label="Open UI configuration"
      title="Configuration UI"
      onClick={openConfigurator}
      onMouseDown={() => setPressed(true)}
      onMouseLeave={() => setPressed(false)}
      onMouseUp={() => setPressed(false)}
      style={{ ...styles.button, ...(pressed ? styles.pressed : undefined) }}
    >
      ⚙
    </button>
  );
}

const styles: Record<string, React.CSSProperties> = {
  button: {
    alignSelf: "stretch",
    border: "1px solid var(--syn-border-medium, rgba(255,255,255,0.2))",
    borderRadius: 4,
    padding: "0 10px",
    background: "var(--syn-nav-panel-bg, rgba(255,255,255,0.08))",
    color: "var(--syn-text-secondary, rgba(232,234,237,0.75))",
    cursor: "pointer",
    fontFamily: "inherit",
    fontWeight: 700,
    fontSize: 14,
    whiteSpace: "nowrap",
    minWidth: 34,
  },
  pressed: {
    background: "var(--syn-nav-button-active-bg, rgba(255,255,255,0.16))",
    color: "var(--syn-text-primary, #e8eaed)",
  },
};
