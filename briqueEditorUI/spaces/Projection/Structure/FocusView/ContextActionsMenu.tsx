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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useBriqueSubstrate } from "../../../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../../../Brique_Substrate/capability/index.js";
import type { ActiveContext, ActiveContextActivationRequest } from "../HierarchyOverview/contracts.js";
import type { ElementItem } from "./contracts.js";

export type ContextActionsMenuProps = {
  activeContext: ActiveContext;
  selectedElement?: ElementItem;
  onActivateContext: (request: ActiveContextActivationRequest) => void;
  onRefresh: () => void;
  onRefreshStructure: () => void;
};

type DialogKind =
  | { type: "create-context" }
  | { type: "delete-context" }
  | { type: "clone-context" }
  | { type: "create-element" }
  | { type: "clone-element" }
  | { type: "delete-element" };

const ELEMENT_KINDS = ["capacity", "matter", "document", "schema", "structure"] as const;

export function ContextActionsMenu({
  activeContext,
  selectedElement,
  onActivateContext,
  onRefresh,
  onRefreshStructure,
}: ContextActionsMenuProps) {
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const [menuOpen, setMenuOpen] = useState(false);
  const [dialog, setDialog] = useState<DialogKind | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const selectedEditableElement =
    selectedElement?.kind === "context" ? undefined : selectedElement;

  useEffect(() => {
    if (!menuOpen) return;
    function handleClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    }
    window.addEventListener("mousedown", handleClick);
    return () => window.removeEventListener("mousedown", handleClick);
  }, [menuOpen]);

  const openDialog = useCallback((kind: DialogKind) => {
    setMenuOpen(false);
    setDialog(kind);
  }, []);

  return (
    <div style={styles.container} ref={menuRef}>
      <button
        style={styles.plusButton}
        onClick={() => setMenuOpen((v) => !v)}
        title="Actions"
      >
        +
      </button>

      {menuOpen && (
        <div style={styles.menu}>
          <div style={styles.menuSection}>Context</div>
          <button style={styles.menuItem} onClick={() => openDialog({ type: "create-context" })}>
            Create context
          </button>
          <button style={styles.menuItem} onClick={() => openDialog({ type: "clone-context" })}>
            Clone context
          </button>
          <button style={{ ...styles.menuItem, ...styles.menuItemDanger }} onClick={() => openDialog({ type: "delete-context" })}>
            Delete context
          </button>
          <div style={styles.menuDivider} />
          <div style={styles.menuSection}>Element</div>
          <button style={styles.menuItem} onClick={() => openDialog({ type: "create-element" })}>
            Create element
          </button>
          {selectedEditableElement && (
            <>
              <button style={styles.menuItem} onClick={() => openDialog({ type: "clone-element" })}>
                Clone element
              </button>
              <button style={{ ...styles.menuItem, ...styles.menuItemDanger }} onClick={() => openDialog({ type: "delete-element" })}>
                Delete element
              </button>
            </>
          )}
        </div>
      )}

      {dialog?.type === "create-context" && (
        <CreateContextDialog
          activeContext={activeContext}
          capabilityClient={capabilityClient}
          onDone={(newKey) => {
            setDialog(null);
            onRefresh();
            if (newKey) onActivateContext({ target: newKey });
          }}
          onCancel={() => setDialog(null)}
        />
      )}

      {dialog?.type === "delete-context" && (
        <DeleteContextDialog
          activeContext={activeContext}
          capabilityClient={capabilityClient}
          onDone={(parentContext) => {
            setDialog(null);
            onRefresh();
            if (parentContext) onActivateContext({ target: parentContext });
          }}
          onCancel={() => setDialog(null)}
        />
      )}

      {dialog?.type === "clone-context" && (
        <CloneContextDialog
          activeContext={activeContext}
          capabilityClient={capabilityClient}
          onDone={() => {
            setDialog(null);
            onRefresh();
          }}
          onCancel={() => setDialog(null)}
        />
      )}

      {dialog?.type === "create-element" && (
        <CreateElementDialog
          activeContext={activeContext}
          capabilityClient={capabilityClient}
          onDone={() => { setDialog(null); onRefreshStructure(); }}
          onCancel={() => setDialog(null)}
        />
      )}

      {dialog?.type === "clone-element" && selectedEditableElement && (
        <CloneElementDialog
          activeContext={activeContext}
          element={selectedEditableElement}
          capabilityClient={capabilityClient}
          onDone={() => { setDialog(null); onRefreshStructure(); }}
          onCancel={() => setDialog(null)}
        />
      )}

      {dialog?.type === "delete-element" && selectedEditableElement && (
        <DeleteElementDialog
          activeContext={activeContext}
          element={selectedEditableElement}
          capabilityClient={capabilityClient}
          onDone={() => { setDialog(null); onRefreshStructure(); }}
          onCancel={() => setDialog(null)}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Dialogs
// ---------------------------------------------------------------------------

type DialogBaseProps = {
  activeContext: ActiveContext;
  capabilityClient: ReturnType<typeof createCapabilityClient>;
  onDone: (result?: string) => void;
  onCancel: () => void;
};

function CreateContextDialog({ activeContext, capabilityClient, onDone, onCancel }: DialogBaseProps) {
  const [name, setName] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);

  async function handleSubmit() {
    if (!name.trim()) return;
    setLoading(true);
    setError(undefined);
    try {
      const result = await capabilityClient.edit.create(activeContext, {
        items: [{ ctx_id: activeContext, element_kind: "context", name: name.trim() }],
      });
      if (!result.ok) {
        setError(result.error?.message ?? "Failed to create context.");
      } else {
        onDone(`${activeContext}/${name.trim()}`);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog title="Create context" onCancel={onCancel}>
      <label style={styles.dialogLabel}>Name</label>
      <input
        style={styles.dialogInput}
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && void handleSubmit()}
        autoFocus
        placeholder="my_context"
      />
      {error && <div style={styles.dialogError}>{error}</div>}
      <div style={styles.dialogActions}>
        <button style={styles.dialogCancel} onClick={onCancel}>Cancel</button>
        <button style={styles.dialogConfirm} onClick={() => void handleSubmit()} disabled={loading || !name.trim()}>
          {loading ? "Creating…" : "Create"}
        </button>
      </div>
    </Dialog>
  );
}

function DeleteContextDialog({ activeContext, capabilityClient, onDone, onCancel }: DialogBaseProps) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const segments = activeContext.split("/").filter(Boolean);
  const label = segments.slice(-1)[0] ?? activeContext;
  const parentContext = segments.length > 1 ? `/${segments.slice(0, -1).join("/")}` : "/root";

  async function handleConfirm() {
    setLoading(true);
    setError(undefined);
    try {
      const result = await capabilityClient.edit.delete(parentContext, {
        items: [{ ctx_id: parentContext, element_kind: "context", name: label }],
      });
      if (!result.ok) {
        setError(result.error?.message ?? "Failed to delete context.");
      } else {
        onDone(parentContext);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog title="Delete context" onCancel={onCancel}>
      <p style={styles.dialogText}>Delete <strong>{label}</strong>? This cannot be undone.</p>
      {error && <div style={styles.dialogError}>{error}</div>}
      <div style={styles.dialogActions}>
        <button style={styles.dialogCancel} onClick={onCancel}>Cancel</button>
        <button style={{ ...styles.dialogConfirm, ...styles.dialogConfirmDanger }} onClick={() => void handleConfirm()} disabled={loading}>
          {loading ? "Deleting…" : "Delete"}
        </button>
      </div>
    </Dialog>
  );
}

function CloneContextDialog({ activeContext, capabilityClient, onDone, onCancel }: DialogBaseProps) {
  const [destination, setDestination] = useState("");
  const segments = activeContext.split("/").filter(Boolean);
  const label = segments.slice(-1)[0] ?? activeContext;
  const parentContext = segments.length > 1 ? `/${segments.slice(0, -1).join("/")}` : "/root";
  const [name, setName] = useState(label);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();

  async function handleSubmit() {
    if (!destination.trim() || !name.trim()) return;
    setLoading(true);
    setError(undefined);
    try {
      const result = await capabilityClient.edit.duplicate(parentContext, {
        items: [{
          ctx_id: parentContext,
          element_kind: "context",
          source_name: label,
          target_name: name.trim(),
          destination_ctx_id: destination.trim(),
        }],
      });
      if (!result.ok) {
        setError(result.error?.message ?? "Failed to clone context.");
      } else {
        onDone();
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog title="Clone context" onCancel={onCancel}>
      <label style={styles.dialogLabel}>Destination context path</label>
      <input
        style={styles.dialogInput}
        value={destination}
        onChange={(e) => setDestination(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && void handleSubmit()}
        autoFocus
        placeholder="/root/app_test"
      />
      <label style={styles.dialogLabel}>Name</label>
      <input
        style={styles.dialogInput}
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && void handleSubmit()}
        placeholder="my_context"
      />
      {error && <div style={styles.dialogError}>{error}</div>}
      <div style={styles.dialogActions}>
        <button style={styles.dialogCancel} onClick={onCancel}>Cancel</button>
        <button style={styles.dialogConfirm} onClick={() => void handleSubmit()} disabled={loading || !destination.trim() || !name.trim()}>
          {loading ? "Cloning…" : "Clone"}
        </button>
      </div>
    </Dialog>
  );
}

function CreateElementDialog({ activeContext, capabilityClient, onDone, onCancel }: DialogBaseProps) {
  const [kind, setKind] = useState<string>(ELEMENT_KINDS[0]);
  const [name, setName] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();

  async function handleSubmit() {
    if (!name.trim()) return;
    setLoading(true);
    setError(undefined);
    try {
      const result =
        kind === "matter"
          ? await capabilityClient.matter.create(activeContext, { matter_id: name.trim() })
          : kind === "structure"
          ? await capabilityClient.structure.create(activeContext, { structure_id: name.trim() })
          : await capabilityClient.edit.create(activeContext, {
              items: [{ ctx_id: activeContext, element_kind: kind, name: name.trim() }],
            });
      if (!result.ok) {
        setError(result.error?.message ?? "Failed to create element.");
      } else {
        onDone();
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog title="Create element" onCancel={onCancel}>
      <label style={styles.dialogLabel}>Kind</label>
      <select style={styles.dialogInput} value={kind} onChange={(e) => setKind(e.target.value)}>
        {ELEMENT_KINDS.map((k) => (
          <option key={k} value={k}>{k}</option>
        ))}
      </select>
      <label style={styles.dialogLabel}>Name</label>
      <input
        style={styles.dialogInput}
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && void handleSubmit()}
        placeholder="my_element"
      />
      {error && <div style={styles.dialogError}>{error}</div>}
      <div style={styles.dialogActions}>
        <button style={styles.dialogCancel} onClick={onCancel}>Cancel</button>
        <button style={styles.dialogConfirm} onClick={() => void handleSubmit()} disabled={loading || !name.trim()}>
          {loading ? "Creating…" : "Create"}
        </button>
      </div>
    </Dialog>
  );
}

type ElementDialogProps = {
  activeContext: ActiveContext;
  element: ElementItem;
  capabilityClient: ReturnType<typeof createCapabilityClient>;
  onDone: () => void;
  onCancel: () => void;
};

function CloneElementDialog({ activeContext, element, capabilityClient, onDone, onCancel }: ElementDialogProps) {
  const [destination, setDestination] = useState("");
  const [name, setName] = useState(element.name);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();

  async function handleSubmit() {
    if (!destination.trim() || !name.trim()) return;
    setLoading(true);
    setError(undefined);
    try {
      if (element.kind === "matter") {
        const result = await capabilityClient.matter.clone(activeContext, {
          source_matter_id: element.name,
          target_matter_id: name.trim(),
          destination_ctx_id: destination.trim(),
        });
        if (!result.ok) {
          setError(result.error?.message ?? "Failed to clone element.");
          return;
        }
      } else if (element.kind === "structure") {
        const result = await capabilityClient.structure.clone(activeContext, {
          source_structure_id: element.name,
          target_structure_id: name.trim(),
          destination_ctx_id: destination.trim(),
        });
        if (!result.ok) {
          setError(result.error?.message ?? "Failed to clone element.");
          return;
        }
      } else {
        const result = await capabilityClient.edit.duplicate(activeContext, {
          items: [{
            ctx_id: activeContext,
            element_kind: element.kind,
            source_name: element.name,
            target_name: name.trim(),
            destination_ctx_id: destination.trim(),
          }],
        });
        if (!result.ok) {
          setError(result.error?.message ?? "Failed to clone element.");
          return;
        }
        const item = result.payload?.result?.[0];
        if (!item?.ok) {
          const itemError = item?.error as { message?: string } | undefined;
          setError(itemError?.message ?? "Failed to clone element.");
          return;
        }
      }
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog title={`Clone element — ${element.name}`} onCancel={onCancel}>
      <label style={styles.dialogLabel}>Destination context path</label>
      <input
        style={styles.dialogInput}
        value={destination}
        onChange={(e) => setDestination(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && void handleSubmit()}
        autoFocus
        placeholder="/root/app_test"
      />
      <label style={styles.dialogLabel}>Name</label>
      <input
        style={styles.dialogInput}
        value={name}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && void handleSubmit()}
        placeholder={element.name}
      />
      {error && <div style={styles.dialogError}>{error}</div>}
      <div style={styles.dialogActions}>
        <button style={styles.dialogCancel} onClick={onCancel}>Cancel</button>
        <button style={styles.dialogConfirm} onClick={() => void handleSubmit()} disabled={loading || !destination.trim() || !name.trim()}>
          {loading ? "Cloning…" : "Clone"}
        </button>
      </div>
    </Dialog>
  );
}

function DeleteElementDialog({ activeContext, element, capabilityClient, onDone, onCancel }: ElementDialogProps) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();

  async function handleConfirm() {
    setLoading(true);
    setError(undefined);
    try {
      const result =
        element.kind === "matter"
          ? await capabilityClient.matter.delete(activeContext, { matter_id: element.name })
          : element.kind === "structure"
          ? await capabilityClient.structure.delete(activeContext, { structure_id: element.name })
          : await capabilityClient.edit.delete(activeContext, {
              items: [{ ctx_id: activeContext, element_kind: element.kind, name: element.name }],
            });
      if (!result.ok) {
        setError(result.error?.message ?? "Failed to delete element.");
      } else {
        onDone();
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog title={`Delete element — ${element.name}`} onCancel={onCancel}>
      <p style={styles.dialogText}>Delete <strong>{element.name}</strong> ({element.kind})? This cannot be undone.</p>
      {error && <div style={styles.dialogError}>{error}</div>}
      <div style={styles.dialogActions}>
        <button style={styles.dialogCancel} onClick={onCancel}>Cancel</button>
        <button style={{ ...styles.dialogConfirm, ...styles.dialogConfirmDanger }} onClick={() => void handleConfirm()} disabled={loading}>
          {loading ? "Deleting…" : "Delete"}
        </button>
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Dialog shell
// ---------------------------------------------------------------------------

function Dialog({ title, onCancel, children }: { title: string; onCancel: () => void; children: React.ReactNode }) {
  return (
    <div style={styles.overlay}>
      <div style={styles.dialogBox}>
        <div style={styles.dialogHeader}>
          <span style={styles.dialogTitle}>{title}</span>
          <button style={styles.dialogClose} onClick={onCancel}>✕</button>
        </div>
        <div style={styles.dialogBody}>{children}</div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const styles: Record<string, React.CSSProperties> = {
  container: {
    position: "relative",
  },
  plusButton: {
    width: "22px",
    height: "22px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: "16px",
    lineHeight: 1,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: 0,
    flexShrink: 0,
  },
  menu: {
    position: "absolute",
    top: "26px",
    left: 0,
    background: "var(--syn-surface-overlay)",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: "6px",
    boxShadow: "0 4px 16px rgba(0,0,0,0.4)",
    zIndex: 200,
    minWidth: "160px",
    padding: "4px 0",
  },
  menuSection: {
    fontSize: "10px",
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.06em",
    opacity: 0.5,
    padding: "6px 12px 2px",
  },
  menuItem: {
    display: "block",
    width: "100%",
    textAlign: "left",
    padding: "6px 12px",
    fontSize: "12px",
    background: "none",
    border: "none",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
  },
  menuItemDanger: {
    color: "var(--syn-feedback-error)",
  },
  menuDivider: {
    height: "1px",
    background: "var(--syn-border-soft)",
    margin: "4px 0",
  },
  overlay: {
    position: "fixed",
    inset: 0,
    background: "rgba(0,0,0,0.52)",
    zIndex: 300,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
  },
  dialogBox: {
    background: "var(--syn-surface-overlay)",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: "8px",
    minWidth: "320px",
    maxWidth: "480px",
    boxShadow: "0 8px 32px rgba(0,0,0,0.6)",
  },
  dialogHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "12px 16px",
    borderBottom: "1px solid var(--syn-border-soft)",
  },
  dialogTitle: {
    fontSize: "13px",
    fontWeight: 700,
  },
  dialogClose: {
    background: "none",
    border: "none",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    opacity: 0.5,
    fontSize: "12px",
  },
  dialogBody: {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    padding: "16px",
  },
  dialogLabel: {
    fontSize: "11px",
    opacity: 0.6,
    textTransform: "uppercase",
    letterSpacing: "0.05em",
  },
  dialogInput: {
    background: "var(--syn-control-bg)",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    color: "var(--syn-text-primary)",
    padding: "6px 10px",
    fontSize: "13px",
    fontFamily: "monospace",
    outline: "none",
    width: "100%",
    boxSizing: "border-box",
  },
  dialogText: {
    margin: 0,
    fontSize: "13px",
    lineHeight: 1.5,
  },
  dialogError: {
    fontSize: "12px",
    color: "var(--syn-feedback-error)",
    padding: "4px 0",
  },
  dialogActions: {
    display: "flex",
    justifyContent: "flex-end",
    gap: "8px",
    marginTop: "4px",
  },
  dialogCancel: {
    padding: "6px 14px",
    fontSize: "12px",
    background: "var(--syn-control-bg)",
    border: "1px solid var(--syn-control-border)",
    borderRadius: "4px",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
  },
  dialogConfirm: {
    padding: "6px 14px",
    fontSize: "12px",
    background: "var(--syn-control-active-bg)",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: "4px",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontWeight: 700,
  },
  dialogConfirmDanger: {
    background: "var(--syn-feedback-error-bg)",
    border: "1px solid var(--syn-feedback-error)",
  },
};
