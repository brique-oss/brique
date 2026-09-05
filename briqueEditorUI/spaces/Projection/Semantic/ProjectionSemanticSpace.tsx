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
import {
  useBriqueMutation,
  useBriqueRaw,
} from "../../../React_Substrate_Adapter/hooks.js";
import type { RawError } from "../../../Brique_Substrate/types/index.js";
import type {
  SavedSemanticWorkspace,
  SavedWorkspaceEntry,
  SavedWorkspaceIndex,
  SemanticBootstrapState,
  SemanticVocabularySnapshot,
  SemanticWindowConfig,
  SemanticWindowSelections,
  VocabularyPath,
} from "./contracts.js";
import { WORKSPACE_REGISTRY_STRUCTURE_ID, workspaceMatterId } from "./contracts.js";
import { SchemaImportDialog } from "./SchemaImport/SchemaImportDialog.js";
import type { SchemaWindowSpec } from "./SchemaImport/schemaImport.js";
import { WorkspaceRestoreDialog } from "./WorkspacePersistence/WorkspaceRestoreDialog.js";
import { WorkspaceSaveDialog } from "./WorkspacePersistence/WorkspaceSaveDialog.js";
import { SemanticVocabularyPathWindow } from "./VocabularyPathWindow/SemanticVocabularyPathWindow.js";

const ROOT_CONTEXT = "/root";

const ELEMENT_KINDS = ["matter", "capacity", "document", "structure", "schema", "context"] as const;

export type ProjectionSemanticSpaceProps = {
  windowSelections: SemanticWindowSelections;
  selectedKinds: Set<string>;
  contextFilterMode: "off" | "strict" | "subtree";
  activeContext?: string;
  onWindowSelectionsChange: (selections: SemanticWindowSelections) => void;
  onSelectedKindsChange: (kinds: Set<string>) => void;
  onContextFilterModeChange: (mode: "off" | "strict" | "subtree") => void;
  onWindowListChange: (windows: SemanticWindowConfig[]) => void;
  onCommit: () => void;
  onReturnToUse: () => void;
  onModeChange: (mode: "edit" | "use") => void;
  inspectMode?: boolean;
};

export function ProjectionSemanticSpace({
  windowSelections,
  selectedKinds,
  contextFilterMode,
  activeContext,
  onWindowSelectionsChange,
  onSelectedKindsChange,
  onContextFilterModeChange,
  onWindowListChange,
  onCommit,
  onReturnToUse,
  onModeChange,
  inspectMode = false,
}: ProjectionSemanticSpaceProps) {
  const key = useMemo(
    () => ({
      context: ROOT_CONTEXT,
      kind: "vocabulary.get",
      id: "root",
    }),
    []
  );
  const vocabulary = useBriqueRaw(key);
  const { mutate } = useBriqueMutation();
  const rebuildStartedRef = useRef(false);
  const [rebuildError, setRebuildError] = useState<unknown>(undefined);
  const [rebuilding, setRebuilding] = useState(false);
  const [rebuildSource, setRebuildSource] = useState<"bootstrap" | "manual" | undefined>(undefined);
  const [windows, setWindows] = useState<SemanticWindowConfig[]>([]);
  const windowsRef = useRef(windows);
  useEffect(() => { windowsRef.current = windows; }, [windows]);
  const [browseMode, setBrowseMode] = useState<"edit" | "use">("edit");
  const mode = inspectMode ? "use" : browseMode;

  const toggleKind = (kind: string) => {
    const next = new Set(selectedKinds);
    if (next.has(kind)) next.delete(kind);
    else next.add(kind);
    onSelectedKindsChange(next);
  };
  const cycleContextFilter = () => {
    const order: Array<"off" | "strict" | "subtree"> = ["off", "strict", "subtree"];
    const next = order[(order.indexOf(contextFilterMode) + 1) % order.length];
    onContextFilterModeChange(next);
  };
  const [persistDialog, setPersistDialog] = useState<null | "save" | "restore">(null);
  const [schemaImportOpen, setSchemaImportOpen] = useState(false);
  const [persistBusy, setPersistBusy] = useState(false);
  const [persistError, setPersistError] = useState<string | null>(null);
  const [savedIndex, setSavedIndex] = useState<SavedWorkspaceEntry[]>([]);
  const nextIdRef = useRef(0);


  // Refs for keydown handler — always current without re-registering.
  const modeRef = useRef(mode);
  const onCommitRef = useRef(onCommit);
  const onReturnToUseRef = useRef(onReturnToUse);
  const onModeChangeRef = useRef(onModeChange);
  useEffect(() => { modeRef.current = mode; }, [mode]);
  useEffect(() => { onCommitRef.current = onCommit; }, [onCommit]);
  useEffect(() => { onReturnToUseRef.current = onReturnToUse; }, [onReturnToUse]);
  useEffect(() => { onModeChangeRef.current = onModeChange; }, [onModeChange]);

  const enterEditMode = useCallback(() => {
    setBrowseMode("edit");
    onModeChangeRef.current("edit");
    onWindowSelectionsChange({});
  }, [onWindowSelectionsChange]);

  const enterUseMode = useCallback(() => {
    setBrowseMode("use");
    onModeChangeRef.current("use");
    onReturnToUseRef.current();
  }, []);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "e" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        if (modeRef.current === "use") {
          enterEditMode();
        } else {
          enterUseMode();
        }
      }
      if (modeRef.current !== "use") return;
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onCommitRef.current();
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enterEditMode, enterUseMode]);

  const loadRegistry = async (): Promise<SavedWorkspaceEntry[]> => {
    const result = await mutate({
      context: ROOT_CONTEXT,
      capability: "structure.read",
      params: { structure_id: WORKSPACE_REGISTRY_STRUCTURE_ID },
    });
    const response = result.response as { status?: string; payload?: { functional?: unknown } } | undefined;
    if (response?.status !== "ok") return [];
    const functional = response?.payload?.functional;
    if (functional && typeof functional === "object" && "version" in functional) {
      const index = functional as SavedWorkspaceIndex;
      if (index.version === 1 && Array.isArray(index.entries)) return index.entries;
    }
    return [];
  };

  const writeRegistry = async (entries: SavedWorkspaceEntry[]) => {
    const index: SavedWorkspaceIndex = { version: 1, entries };
    const read = await mutate({
      context: ROOT_CONTEXT,
      capability: "structure.read",
      params: { structure_id: WORKSPACE_REGISTRY_STRUCTURE_ID },
    });
    const exists = (read.response as { status?: string } | undefined)?.status === "ok";
    if (exists) {
      await mutate({
        context: ROOT_CONTEXT,
        capability: "structure.patch",
        params: {
          structure_id: WORKSPACE_REGISTRY_STRUCTURE_ID,
          patches: [{ op: "replace", path: ["functional", "entries"], value: entries }],
        },
      });
    } else {
      await mutate({
        context: ROOT_CONTEXT,
        capability: "structure.create",
        params: {
          structure_id: WORKSPACE_REGISTRY_STRUCTURE_ID,
          structure: { functional: index as unknown as Record<string, unknown> },
        },
      });
    }
  };

  const openSaveDialog = async () => {
    setPersistError(null);
    try {
      const entries = await loadRegistry();
      setSavedIndex(entries);
    } catch {
      setSavedIndex([]);
    }
    setPersistDialog("save");
  };

  const openRestoreDialog = async () => {
    setPersistError(null);
    try {
      const entries = await loadRegistry();
      setSavedIndex(entries);
    } catch {
      setSavedIndex([]);
    }
    setPersistDialog("restore");
  };

  const confirmSave = async (name: string) => {
    setPersistBusy(true);
    setPersistError(null);
    try {
      const matterId = workspaceMatterId(name);
      const payload: SavedSemanticWorkspace = {
        version: 1,
        mode,
        windows: windows.map((w) => ({ id: w.id, path: w.path, pos: w.pos, size: w.size, defaultKeys: w.defaultKeys, defaultCombinator: w.defaultCombinator })),
      };
      const bytes = btoa(JSON.stringify(payload));
      const exists = await mutate({
        context: ROOT_CONTEXT,
        capability: "matter.exists",
        params: { matter_id: matterId },
      });
      const exist = (exists.response as { payload?: { exist?: boolean } } | undefined)?.payload?.exist;
      await mutate({
        context: ROOT_CONTEXT,
        capability: exist ? "matter.write" : "matter.create",
        params: { matter_id: matterId, data: { kind: "inline", bytes } },
      });
      const current = await loadRegistry();
      const next = current.filter((e) => e.name !== name);
      next.push({ name, matterId });
      await writeRegistry(next);
      setSavedIndex(next);
      setPersistDialog(null);
    } catch (err) {
      console.error("[save] failed", String(err));
      setPersistError(String(err));
    } finally {
      setPersistBusy(false);
    }
  };

  const confirmRestore = async (entry: SavedWorkspaceEntry) => {
    setPersistBusy(true);
    setPersistError(null);
    try {
      const result = await mutate({
        context: ROOT_CONTEXT,
        capability: "matter.read",
        params: { matter_id: entry.matterId, want_data: true },
      });
      console.log("[restore] matter.read response", JSON.stringify(result.response, null, 2));
      const data = (result.response as { payload?: { data?: { kind?: string; bytes?: string } } } | undefined)?.payload?.data;
      console.log("[restore] data", data);
      if (data?.kind === "inline" && data.bytes) {
        const outer = JSON.parse(atob(data.bytes)) as { kind?: string; bytes?: string };
        const innerBytes = outer.kind === "inline" && outer.bytes ? outer.bytes : data.bytes;
        const saved = JSON.parse(atob(innerBytes)) as SavedSemanticWorkspace;
        console.log("[restore] parsed", saved);
        if (saved.version === 1 && Array.isArray(saved.windows)) {
          // windowId = path.join(".") — reconstruct from path
          const vocab = state.kind === "ready" ? state.snapshot.vocabulary : {};
          const restoredWindows: SemanticWindowConfig[] = saved.windows.map((w) => ({
            id: w.path.join(".") || `__new_${nextIdRef.current++}`,
            path: w.path,
            pos: w.pos,
            size: w.size,
            valueLevel: Array.isArray(getVocabularyPathValue(vocab, w.path)),
            defaultKeys: w.defaultKeys ?? [],
            defaultCombinator: w.defaultCombinator ?? "OR",
          }));
          const restoredMode: "edit" | "use" = saved.mode === "use" ? "use" : "edit";
          setWindows(restoredWindows);
          setBrowseMode(restoredMode);
          onModeChangeRef.current(restoredMode);
          onWindowListChange(restoredWindows);
          onWindowSelectionsChange({});
        }
      }
      setPersistDialog(null);
    } catch (err) {
      console.error("[restore] error", err);
      setPersistError("Restore failed.");
    } finally {
      setPersistBusy(false);
    }
  };

  const updateWindows = (next: SemanticWindowConfig[]) => {
    setWindows(next);
    onWindowListChange(next);
  };

  const getValueLevel = (path: VocabularyPath): boolean => {
    if (state.kind !== "ready") return false;
    return Array.isArray(getVocabularyPathValue(state.snapshot.vocabulary, path));
  };

  const addWindow = () => {
    const n = nextIdRef.current++;
    const offset = n * 24;
    const tempId = `__new_${n}`;
    updateWindows([...windows, {
      id: tempId,
      path: [],
      pos: { x: 40 + offset, y: 40 + offset },
      size: { width: 380, height: 430 },
      valueLevel: false,
      defaultKeys: [],
      defaultCombinator: "OR",
    }]);
  };
  const clearWindows = () => {
    updateWindows([]);
    onWindowSelectionsChange({});
  };

  const SCHEMA_COLS = 3;
  const SCHEMA_MARGIN = 40;
  const SCHEMA_GAP = 20;
  const SCHEMA_W = 380;
  const SCHEMA_H = 430;

  const handleSchemaApply = (specs: SchemaWindowSpec[]) => {
    const vocab = state.kind === "ready" ? state.snapshot.vocabulary : {};
    const next: SemanticWindowConfig[] = specs.map((spec, i) => ({
      id: spec.path.join(".") || `__new_${nextIdRef.current++}`,
      path: spec.path,
      valueLevel: Array.isArray(getVocabularyPathValue(vocab, spec.path)),
      defaultKeys: spec.defaultKeys,
      defaultCombinator: "OR" as const,
      pos: {
        x: SCHEMA_MARGIN + (i % SCHEMA_COLS) * (SCHEMA_W + SCHEMA_GAP),
        y: SCHEMA_MARGIN + Math.floor(i / SCHEMA_COLS) * (SCHEMA_H + SCHEMA_GAP),
      },
      size: { width: SCHEMA_W, height: SCHEMA_H },
    }));
    updateWindows(next);
    onWindowSelectionsChange({});
    setSchemaImportOpen(false);
  };

  const closeWindow = (id: string) => {
    updateWindows(windows.filter((w) => w.id !== id));
    const next = { ...windowSelections };
    delete next[id];
    onWindowSelectionsChange(next);
  };
  const updateWindowPath = (id: string, path: VocabularyPath) => {
    const newId = path.join(".");
    const next = windows.map((w) =>
      w.id === id
        ? { ...w, id: newId, path, valueLevel: getValueLevel(path), defaultKeys: [], defaultCombinator: "OR" as const }
        : w
    );
    updateWindows(next);
    if (id !== newId) {
      const nextSel = { ...windowSelections };
      delete nextSel[id];
      onWindowSelectionsChange(nextSel);
    }
  };
  const updateWindowPos = (id: string, pos: { x: number; y: number }) => {
    setWindows((current) => {
      const next = current.map((w) => (w.id === id ? { ...w, pos } : w));
      onWindowListChange(next);
      return next;
    });
  };
  const updateWindowSize = (id: string, size: { width: number; height: number }) => {
    setWindows((current) => {
      const next = current.map((w) => (w.id === id ? { ...w, size } : w));
      onWindowListChange(next);
      return next;
    });
  };

  const updateWindowSelection = (
    id: string,
    selectedKeys: string[],
    combinator: "AND" | "OR"
  ) => {
    if (mode === "edit") {
      updateWindows(windows.map((w) =>
        w.id === id ? { ...w, defaultKeys: selectedKeys, defaultCombinator: combinator } : w
      ));
    } else {
      onWindowSelectionsChange({ ...windowSelections, [id]: { selectedKeys, combinator } });
    }
  };

  const addVocabularyValue = useCallback(async (path: VocabularyPath, value: string) => {
    const trimmed = value.trim();
    if (!trimmed || path.length === 0) return;
    await mutate({
      context: ROOT_CONTEXT,
      capability: "vocabulary.patch",
      params: { patch: { add: [{ path, value: trimmed }] } },
    });
    await vocabulary.reload();
  }, [mutate, vocabulary]);

  const removeVocabularyValues = useCallback(async (path: VocabularyPath, values: string[]) => {
    const selected = values.map((value) => value.trim()).filter(Boolean);
    if (selected.length === 0 || path.length === 0) return;
    await mutate({
      context: ROOT_CONTEXT,
      capability: "vocabulary.patch",
      params: {
        patch: {
          remove: selected.map((value) => ({ path, value })),
        },
      },
    });
    await vocabulary.reload();
  }, [mutate, vocabulary]);

  const rebuildMeaning = useCallback(async (source: "bootstrap" | "manual") => {
    setRebuildError(undefined);
    setRebuildSource(source);
    setRebuilding(true);
    try {
      const result = await mutate({
        context: ROOT_CONTEXT,
        capability: "meaning.rebuild",
        params: { mode: "full" },
      });
      const response = result.response as { status?: string; error?: { message?: string; code?: string } } | undefined;
      if (response?.status !== "ok") {
        throw new Error(response?.error?.message ?? response?.error?.code ?? "Unable to rebuild semantic vocabulary.");
      }
      await vocabulary.reload();
    } catch (err) {
      setRebuildError(err);
    } finally {
      setRebuilding(false);
      setRebuildSource(undefined);
    }
  }, [mutate, vocabulary.reload]);

  useEffect(() => {
    if (vocabulary.loading || rebuilding || rebuildStartedRef.current) return;
    if (!shouldBootstrapRebuild(vocabulary.data)) return;

    rebuildStartedRef.current = true;
    void rebuildMeaning("bootstrap");
  }, [rebuildMeaning, rebuilding, vocabulary.data, vocabulary.loading]);

  const state = getBootstrapState({
    loading: vocabulary.loading,
    raw: vocabulary.data,
    rawError: vocabulary.error,
    rebuildError,
    rebuildSource,
    rebuilding,
  });
  const readyVocabulary = state.kind === "ready" ? state.snapshot.vocabulary : undefined;

  useEffect(() => {
    if (!readyVocabulary) return;

    setWindows((current) => {
      let changed = false;
      const next = current.map((w) => {
        const valueLevel = Array.isArray(getVocabularyPathValue(readyVocabulary, w.path));
        if (w.valueLevel === valueLevel) return w;
        changed = true;
        return { ...w, valueLevel };
      });
      if (changed) onWindowListChange(next);
      return changed ? next : current;
    });
  }, [readyVocabulary, onWindowListChange]);

  return (
    <section
      aria-label="Brique Editor semantic projection"
      data-space="projection-semantic"
      style={styles.root}
    >
      <header style={styles.header}>
        <span style={styles.title}>Semantic Projection</span>
        <div style={styles.headerActions}>
          <button
            disabled={rebuilding || vocabulary.loading}
            onClick={() => void rebuildMeaning("manual")}
            style={styles.headerButton}
            title="Rebuild meaning projection"
            type="button"
          >
            {rebuilding ? "Refreshing" : "Refresh"}
          </button>
          {mode === "edit" ? (
            <>
              {state.kind === "ready" && (
                <button onClick={addWindow} style={styles.headerButton} title="Add semantic window" type="button">
                  +
                </button>
              )}
              <button
                onClick={() => setSchemaImportOpen(true)}
                style={styles.headerButton}
                title="Import windows from schema"
                type="button"
              >
                Schema
              </button>
              <button
                onClick={clearWindows}
                style={styles.headerButton}
                title="Remove all windows"
                type="button"
              >
                Clear
              </button>
              <button
                disabled={persistBusy}
                onClick={openSaveDialog}
                style={styles.headerButton}
                title="Save workspace"
                type="button"
              >
                Save
              </button>
              <button
                disabled={persistBusy}
                onClick={openRestoreDialog}
                style={styles.headerButton}
                title="Restore workspace"
                type="button"
              >
                Open
              </button>
              {persistError && (
                <span onClick={() => setPersistError(null)} style={styles.persistError} title={persistError}>!</span>
              )}
            </>
          ) : !inspectMode ? (
            <>
              <div style={styles.kindGroup}>
                {ELEMENT_KINDS.map((kind) => (
                  <button
                    key={kind}
                    onClick={() => toggleKind(kind)}
                    style={{ ...styles.kindButton, ...(selectedKinds.has(kind) ? styles.kindButtonActive : {}) }}
                    title={`Filter by ${kind}`}
                    type="button"
                  >
                    {kind}
                  </button>
                ))}
              </div>
              <button
                disabled={!activeContext}
                onClick={cycleContextFilter}
                style={{ ...styles.headerButton, ...(contextFilterMode !== "off" ? styles.headerButtonActive : {}) }}
                title={
                  activeContext
                    ? `Context filter: ${contextFilterMode} (${activeContext})`
                    : "No active context (select one in Structure)"
                }
                type="button"
              >
                {contextFilterMode === "off" ? "Context" : `Context: ${contextFilterMode}`}
              </button>
            </>
          ) : null}
          {!inspectMode && (
            <button
              onClick={() => mode === "use" ? enterEditMode() : enterUseMode()}
              style={{ ...styles.headerButton, ...(mode === "use" ? styles.headerButtonActive : {}) }}
              type="button"
            >
              {mode === "edit" ? "Use" : "Edit"}
            </button>
          )}
        </div>
      </header>
      <SemanticBootstrapView
        windows={windows}
        mode={mode}
        snapshot={state.kind === "ready" ? state.snapshot : undefined}
        state={state}
        windowSelections={windowSelections}
        onCloseWindow={closeWindow}
        onWindowPathChange={updateWindowPath}
        onWindowPosChange={updateWindowPos}
        onWindowSizeChange={updateWindowSize}
        onWindowSelectionChange={updateWindowSelection}
        onVocabularyValueAdd={addVocabularyValue}
        onVocabularyValuesRemove={removeVocabularyValues}
      />
      {persistDialog === "save" && (
        <WorkspaceSaveDialog
          busy={persistBusy}
          existing={savedIndex}
          onCancel={() => setPersistDialog(null)}
          onConfirm={confirmSave}
        />
      )}
      {persistDialog === "restore" && (
        <WorkspaceRestoreDialog
          busy={persistBusy}
          entries={savedIndex}
          onCancel={() => setPersistDialog(null)}
          onConfirm={confirmRestore}
        />
      )}
      {schemaImportOpen && (
        <SchemaImportDialog
          mutate={mutate}
          onApply={handleSchemaApply}
          onClose={() => setSchemaImportOpen(false)}
        />
      )}
    </section>
  );
}

type SemanticWindow = {
  id: string;
  path: VocabularyPath;
  pos: { x: number; y: number };
  size: { width: number; height: number };
  defaultKeys: string[];
  defaultCombinator: "AND" | "OR";
};

function SemanticBootstrapView({
  state,
  windows,
  mode,
  snapshot,
  windowSelections,
  onCloseWindow,
  onWindowPathChange,
  onWindowPosChange,
  onWindowSizeChange,
  onWindowSelectionChange,
  onVocabularyValueAdd,
  onVocabularyValuesRemove,
}: {
  state: SemanticBootstrapState;
  windows: SemanticWindow[];
  mode: "edit" | "use";
  snapshot: SemanticVocabularySnapshot | undefined;
  windowSelections: Record<string, { selectedKeys: string[]; combinator: "AND" | "OR" }>;
  onCloseWindow: (id: string) => void;
  onWindowPathChange: (id: string, path: VocabularyPath) => void;
  onWindowPosChange: (id: string, pos: { x: number; y: number }) => void;
  onWindowSizeChange: (id: string, size: { width: number; height: number }) => void;
  onWindowSelectionChange: (id: string, selectedKeys: string[], combinator: "AND" | "OR") => void;
  onVocabularyValueAdd: (path: VocabularyPath, value: string) => Promise<void>;
  onVocabularyValuesRemove: (path: VocabularyPath, values: string[]) => Promise<void>;
}) {
  if (state.kind === "ready" && snapshot) {
    return (
      <SemanticWorkspaceCanvas
        windows={windows}
        mode={mode}
        snapshot={snapshot}
        windowSelections={windowSelections}
        onCloseWindow={onCloseWindow}
        onWindowPathChange={onWindowPathChange}
        onWindowPosChange={onWindowPosChange}
        onWindowSizeChange={onWindowSizeChange}
        onWindowSelectionChange={onWindowSelectionChange}
        onVocabularyValueAdd={onVocabularyValueAdd}
        onVocabularyValuesRemove={onVocabularyValuesRemove}
      />
    );
  }

  return (
    <div style={styles.bootstrap}>
      <div style={styles.statusKind}>{state.kind}</div>
      <div style={styles.statusMessage}>{state.message}</div>
    </div>
  );
}

const SCROLLBAR_SIZE = 6;
const SCROLLBAR_MARGIN = 4;

const canvasStyles: Record<string, React.CSSProperties> = {
  viewport: {
    position: "relative",
    width: "100%",
    height: "100%",
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
    background: "var(--syn-semantic-bg)",
    userSelect: "none",
  },
  scrollTrackH: {
    position: "absolute",
    bottom: SCROLLBAR_MARGIN,
    left: SCROLLBAR_MARGIN,
    right: SCROLLBAR_MARGIN + SCROLLBAR_SIZE + SCROLLBAR_MARGIN,
    height: SCROLLBAR_SIZE,
    borderRadius: SCROLLBAR_SIZE,
    background: "var(--syn-border-soft)",
    pointerEvents: "none",
    zIndex: 300,
  },
  scrollTrackV: {
    position: "absolute",
    right: SCROLLBAR_MARGIN,
    top: SCROLLBAR_MARGIN,
    bottom: SCROLLBAR_MARGIN + SCROLLBAR_SIZE + SCROLLBAR_MARGIN,
    width: SCROLLBAR_SIZE,
    borderRadius: SCROLLBAR_SIZE,
    background: "var(--syn-border-soft)",
    pointerEvents: "none",
    zIndex: 300,
  },
  scrollThumb: {
    position: "absolute",
    borderRadius: SCROLLBAR_SIZE,
    background: "var(--syn-selected-border)",
    pointerEvents: "none",
  },
};

function SemanticWorkspaceCanvas({
  snapshot,
  windows,
  mode,
  windowSelections,
  onCloseWindow,
  onWindowPathChange,
  onWindowPosChange,
  onWindowSizeChange,
  onWindowSelectionChange,
  onVocabularyValueAdd,
  onVocabularyValuesRemove,
}: {
  snapshot: SemanticVocabularySnapshot;
  windows: SemanticWindow[];
  mode: "edit" | "use";
  windowSelections: Record<string, { selectedKeys: string[]; combinator: "AND" | "OR" }>;
  onCloseWindow: (id: string) => void;
  onWindowPathChange: (id: string, path: VocabularyPath) => void;
  onWindowPosChange: (id: string, pos: { x: number; y: number }) => void;
  onWindowSizeChange: (id: string, size: { width: number; height: number }) => void;
  onWindowSelectionChange: (id: string, selectedKeys: string[], combinator: "AND" | "OR") => void;
  onVocabularyValueAdd: (path: VocabularyPath, value: string) => Promise<void>;
  onVocabularyValuesRemove: (path: VocabularyPath, values: string[]) => Promise<void>;
}) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [scale, setScale] = useState(1);
  const [panning, setPanning] = useState(false);
  const [vpSize, setVpSize] = useState({ w: 0, h: 0 });
  const dragRef = useRef<{ startX: number; startY: number; originX: number; originY: number } | null>(null);
  const scaleRef = useRef(scale);
  const offsetRef = useRef(offset);
  scaleRef.current = scale;
  offsetRef.current = offset;

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    function onWheel(event: WheelEvent) {
      if ((event.target as Element).closest("[data-semantic-window]")) return;
      event.preventDefault();
      if (event.ctrlKey) {
        const factor = event.deltaY > 0 ? 0.9 : 1.1;
        const nextScale = Math.min(4, Math.max(0.25, scaleRef.current * factor));
        if (nextScale === scaleRef.current) return;
        const rect = viewport!.getBoundingClientRect();
        const px = event.clientX - rect.left;
        const py = event.clientY - rect.top;
        const cx = (px - offsetRef.current.x) / scaleRef.current;
        const cy = (py - offsetRef.current.y) / scaleRef.current;
        setScale(nextScale);
        setOffset({ x: px - cx * nextScale, y: py - cy * nextScale });
      } else {
        setOffset((cur) => ({ x: cur.x - event.deltaX, y: cur.y - event.deltaY }));
      }
    }
    viewport.addEventListener("wheel", onWheel, { passive: false });
    return () => viewport.removeEventListener("wheel", onWheel);
  }, []);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;
      setVpSize({ w: entry.contentRect.width, h: entry.contentRect.height });
    });
    observer.observe(viewport);
    return () => observer.disconnect();
  }, []);

  const scrollbars = (() => {
    if (vpSize.w === 0 || vpSize.h === 0 || windows.length === 0) return null;
    const vw = vpSize.w;
    const vh = vpSize.h;
    // compute bounding box of all windows in screen coords
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const w of windows) {
      const sx = w.pos.x * scale + offset.x;
      const sy = w.pos.y * scale + offset.y;
      if (sx < minX) minX = sx;
      if (sy < minY) minY = sy;
      if (sx + w.size.width * scale > maxX) maxX = sx + w.size.width * scale;
      if (sy + w.size.height * scale > maxY) maxY = sy + w.size.height * scale;
    }
    const MARGIN = 40;
    const totalLeft = Math.max(0, -minX + MARGIN);
    const totalRight = Math.max(0, maxX - vw + MARGIN);
    const totalTop = Math.max(0, -minY + MARGIN);
    const totalBottom = Math.max(0, maxY - vh + MARGIN);
    const overflowH = totalLeft + totalRight > 0;
    const overflowV = totalTop + totalBottom > 0;
    if (!overflowH && !overflowV) return null;

    const totalW = vw + totalLeft + totalRight;
    const totalH = vh + totalTop + totalBottom;
    const scrollX = totalLeft;
    const scrollY = totalTop;
    const thumbWRatio = Math.min(1, vw / totalW);
    const thumbHRatio = Math.min(1, vh / totalH);
    const thumbXPct = totalW > vw ? scrollX / (totalW - vw) : 0;
    const thumbYPct = totalH > vh ? scrollY / (totalH - vh) : 0;

    return { showH: overflowH, showV: overflowV, thumbXPct, thumbYPct, thumbWRatio, thumbHRatio };
  })();

  return (
    <div
      ref={viewportRef}
      style={{ ...canvasStyles.viewport, cursor: panning ? "grabbing" : "default" }}
      onPointerDown={(event) => {
        if (event.button !== 0) return;
        if ((event.target as Element).closest("[data-semantic-window]")) return;
        if ((event.target as Element).closest("button,input,[role='button'],a")) return;
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
      onPointerCancel={() => {
        dragRef.current = null;
        setPanning(false);
      }}
    >
      {windows.map((w) => {
        const sel = windowSelections[w.id];
        const selectedKeys = mode === "edit" ? w.defaultKeys : (sel?.selectedKeys ?? []);
        const combinator = mode === "edit" ? w.defaultCombinator : (sel?.combinator ?? "OR");
        return (
          <SemanticVocabularyPathWindow
            key={w.id}
            path={w.path}
            mode={mode}
            selectedKeys={selectedKeys}
            combinator={combinator}
            pos={w.pos}
            canvasOffset={offset}
            scale={scale}
            size={w.size}
            snapshot={snapshot}
            onClose={() => onCloseWindow(w.id)}
            onPathChange={(path) => onWindowPathChange(w.id, path)}
            onPosChange={(pos) => onWindowPosChange(w.id, pos)}
            onSizeChange={(size) => onWindowSizeChange(w.id, size)}
            onSelectionChange={(keys, comb) => onWindowSelectionChange(w.id, keys, comb)}
            onVocabularyValueAdd={onVocabularyValueAdd}
            onVocabularyValuesRemove={async (path, values) => {
              await onVocabularyValuesRemove(path, values);
              if (mode === "edit") {
                onWindowSelectionChange(w.id, w.defaultKeys.filter((k) => !values.includes(k)), w.defaultCombinator);
              } else {
                onWindowSelectionChange(w.id, (sel?.selectedKeys ?? []).filter((k) => !values.includes(k)), combinator);
              }
            }}
          />
        );
      })}
      {scrollbars?.showH && (
        <div style={canvasStyles.scrollTrackH}>
          <div style={{
            ...canvasStyles.scrollThumb,
            left:   `${scrollbars.thumbXPct * (1 - scrollbars.thumbWRatio) * 100}%`,
            width:  `${scrollbars.thumbWRatio * 100}%`,
            top: 0,
            bottom: 0,
          }} />
        </div>
      )}
      {scrollbars?.showV && (
        <div style={canvasStyles.scrollTrackV}>
          <div style={{
            ...canvasStyles.scrollThumb,
            top:    `${scrollbars.thumbYPct * (1 - scrollbars.thumbHRatio) * 100}%`,
            height: `${scrollbars.thumbHRatio * 100}%`,
            left: 0,
            right: 0,
          }} />
        </div>
      )}
    </div>
  );
}

function shouldBootstrapRebuild(
  raw: ReturnType<typeof useBriqueRaw>["data"]
): boolean {
  if (!raw) return false;
  if (raw.status === "absent" || raw.status === "error") return true;
  return readVocabularySnapshot(raw.payload) === undefined;
}

function getBootstrapState(input: {
  loading: boolean;
  raw: ReturnType<typeof useBriqueRaw>["data"];
  rawError: unknown;
  rebuildError: unknown;
  rebuildSource: "bootstrap" | "manual" | undefined;
  rebuilding: boolean;
}): SemanticBootstrapState {
  if (input.rebuildError) {
    return {
      kind: "error",
      message: errorMessage(input.rebuildError, "Unable to rebuild semantic vocabulary."),
    };
  }

  if (input.rebuilding) {
    return {
      kind: "rebuilding",
      message: input.rebuildSource === "manual"
        ? "Refreshing meaning projection..."
        : "Vocabulary is unavailable. Rebuilding meaning projection...",
    };
  }

  if (input.rawError) {
    return {
      kind: "error",
      message: errorMessage(input.rawError, "Unable to request vocabulary."),
    };
  }

  if (input.loading || !input.raw) {
    return {
      kind: "loading",
      message: "Loading semantic vocabulary...",
    };
  }

  if (input.raw.status === "error") {
    return {
      kind: "error",
      message: rawErrorMessage(input.raw.error, "Semantic vocabulary is unavailable."),
    };
  }

  if (input.raw.status === "absent") {
    return {
      kind: "error",
      message: "Semantic vocabulary capability is unavailable.",
    };
  }

  const snapshot = readVocabularySnapshot(input.raw.payload);
  if (!snapshot) {
    return {
      kind: "error",
      message: "Vocabulary response does not contain a vocabulary object.",
    };
  }

  return {
    kind: "ready",
    message: "Vocabulary snapshot loaded.",
    snapshot,
  };
}

function readVocabularySnapshot(
  payload: unknown
): SemanticVocabularySnapshot | undefined {
  if (!isRecord(payload) || !isRecord(payload.vocabulary)) return undefined;
  return { vocabulary: payload.vocabulary };
}

function rawErrorMessage(error: RawError, fallback: string): string {
  return error.message ?? error.code ?? fallback;
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function getVocabularyPathValue(vocab: Record<string, unknown>, path: VocabularyPath): unknown {
  let cursor: unknown = vocab;
  for (const segment of path) {
    if (!isRecord(cursor)) return undefined;
    cursor = cursor[segment];
  }
  return cursor;
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    display: "grid",
    gridTemplateRows: "auto 1fr",
    width: "100%",
    height: "100%",
    minWidth: 0,
    minHeight: 0,
    boxSizing: "border-box",
    overflow: "hidden",
    background: "var(--syn-semantic-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
  },
  header: {
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "0 12px",
    height: 38,
    borderBottom: "1px solid var(--syn-border-soft)",
    background: "var(--syn-semantic-panel-bg)",
    flexShrink: 0,
  },
  title: {
    flex: "1 1 0%",
    color: "var(--syn-nav-title)",
    fontSize: 11,
    fontWeight: 900,
    letterSpacing: "0.12em",
    textTransform: "uppercase",
  },
  headerActions: {
    display: "flex",
    alignItems: "center",
    gap: 6,
  },
  headerButton: {
    height: 26,
    padding: "0 10px",
    display: "grid",
    placeItems: "center",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 5,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    fontSize: 11,
    fontWeight: 900,
    letterSpacing: "0.06em",
  },
  headerButtonActive: {
    background: "var(--syn-selected-bg)",
    borderColor: "var(--syn-selected-border)",
    color: "var(--syn-nav-title)",
  },
  kindGroup: {
    display: "flex",
    alignItems: "center",
    gap: 2,
    padding: "0 4px",
    borderLeft: "1px solid var(--syn-border-soft)",
    borderRight: "1px solid var(--syn-border-soft)",
  },
  kindButton: {
    height: 22,
    padding: "0 7px",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 4,
    background: "transparent",
    color: "var(--syn-text-muted)",
    cursor: "pointer",
    fontSize: 10,
    fontWeight: 900,
    letterSpacing: "0.06em",
    textTransform: "uppercase" as const,
  },
  kindButtonActive: {
    background: "var(--syn-selected-bg)",
    borderColor: "var(--syn-selected-border)",
    color: "var(--syn-nav-title)",
  },
  persistError: {
    width: 20,
    height: 20,
    display: "grid",
    placeItems: "center",
    borderRadius: "50%",
    background: "var(--syn-feedback-error-bg)",
    color: "var(--syn-feedback-error)",
    fontSize: 12,
    fontWeight: 900,
    cursor: "pointer",
    flexShrink: 0,
  },
  bootstrap: {
    alignSelf: "center",
    justifySelf: "center",
    display: "grid",
    gap: "12px",
    minWidth: "320px",
    maxWidth: "680px",
    padding: "22px",
    border: "1px solid var(--syn-border-medium)",
    borderRadius: "12px",
    background: "var(--syn-semantic-panel-bg)",
    boxShadow: "0 20px 70px rgba(0,0,0,0.22)",
  },
  workspace: {
    position: "relative",
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
  },
  canvas: {
    position: "relative",
    width: "100%",
    height: "100%",
    overflow: "hidden",
    background: "var(--syn-semantic-bg)",
  },
  plusButton: {
    position: "absolute",
    top: 14,
    left: 14,
    width: 34,
    height: 34,
    display: "grid",
    placeItems: "center",
    border: "1px solid var(--syn-control-border)",
    borderRadius: 6,
    background: "var(--syn-control-bg)",
    color: "var(--syn-text-primary)",
    cursor: "pointer",
    zIndex: 120,
  },
  semanticWindow: {
    position: "absolute",
    left: 92,
    top: 86,
    width: 300,
    minHeight: 112,
    display: "grid",
    alignContent: "start",
    gap: 8,
    padding: 14,
    border: "1px solid var(--syn-semantic-window-border)",
    borderRadius: 8,
    background: "var(--syn-semantic-window-bg)",
    boxShadow: "0 18px 50px rgba(0,0,0,0.22)",
  },
  semanticWindowLabel: {
    color: "var(--syn-nav-title)",
    fontSize: 10,
    fontWeight: 900,
    textTransform: "uppercase",
  },
  semanticWindowPath: {
    minWidth: 0,
    overflowWrap: "anywhere",
    color: "var(--syn-text-primary)",
    fontSize: 17,
    lineHeight: 1.25,
  },
  semanticWindowHint: {
    color: "var(--syn-text-secondary)",
    fontSize: 12,
    lineHeight: 1.35,
  },
  statusKind: {
    color: "var(--syn-nav-title)",
    fontSize: "11px",
    fontWeight: 900,
    letterSpacing: "0.12em",
    textTransform: "uppercase",
  },
  statusMessage: {
    color: "var(--syn-text-primary)",
    fontSize: "14px",
    lineHeight: 1.5,
  },
  roots: {
    display: "flex",
    flexWrap: "wrap",
    gap: "8px",
  },
  rootChip: {
    padding: "6px 9px",
    border: "1px solid var(--syn-selected-border)",
    borderRadius: "999px",
    background: "var(--syn-selected-bg)",
    color: "var(--syn-text-secondary)",
    fontSize: "11px",
    fontWeight: 800,
  },
};
