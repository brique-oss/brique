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
import { useHostBridge } from "../../HostBridge/context.js";
import { useBriqueSubstrate } from "../../React_Substrate_Adapter/context.js";
import { createCapabilityClient } from "../../Brique_Substrate/capability/index.js";
import { BrowseView } from "./BrowseView.js";
import { toPosixPath } from "../shared/pathUtils.js";
import { InspectView } from "./InspectView.js";
import type { SearchState } from "./SearchView.js";
import { InspectionDisplayControl } from "./InspectionDisplayControl.js";
import { InspectionModeControl } from "./InspectionModeControl.js";
import { SearchView } from "./SearchView.js";
import type {
  CurrentSemanticFilter,
  SemanticWindowConfig,
  SemanticWindowSelections,
  SemanticWorkspaceMode,
  VocabularyPath,
} from "../Projection/Semantic/contracts.js";
import type { ProjectionKind } from "../Projection/contracts.js";
import type {
  InspectionDisplayMode,
  InspectionMode,
  InspectionReadResult,
  InspectionReadState,
  InspectionTarget,
} from "./contracts.js";

const ROOT_CONTEXT = "/root";

export type InspectionSpaceProps = {
  displayMode: InspectionDisplayMode;
  contextFilterMode: "off" | "strict" | "subtree";
  onDisplayModeChange: (mode: InspectionDisplayMode) => void;
  target: InspectionTarget | undefined;
  windowSelections: SemanticWindowSelections;
  selectedKinds: Set<string>;
  windowList: SemanticWindowConfig[];
  activeProjection: ProjectionKind;
  semanticWorkspaceMode: SemanticWorkspaceMode;
  onInspectionTargetChange: (target: InspectionTarget | undefined) => void;
  onBrowseRestore: (browseFilter: SemanticWindowSelections) => void;
  onInspectSeed: (seed: SemanticWindowSelections) => void;
  onInspectionModeChange: (mode: InspectionMode) => void;
  onActivateContext: (contextPath: string) => void;
  onOpenInNewTab?: (target: InspectionTarget) => void;
};

export function InspectionSpace({
  displayMode,
  contextFilterMode,
  onDisplayModeChange,
  target,
  windowSelections,
  selectedKinds,
  windowList,
  activeProjection,
  semanticWorkspaceMode,
  onInspectionTargetChange,
  onBrowseRestore,
  onInspectSeed,
  onInspectionModeChange,
  onActivateContext,
  onOpenInNewTab,
}: InspectionSpaceProps) {
  const { hostBridge, contextPath } = useHostBridge();
  const substrate = useBriqueSubstrate();
  const capabilityClient = useMemo(() => createCapabilityClient(substrate), [substrate]);
  const requestSequence = useRef(0);
  const [mode, setMode] = useState<InspectionMode>("inspect");
  const [readState, setReadState] = useState<InspectionReadState>({
    target: undefined,
    capability: undefined,
    result: undefined,
    loading: false,
    error: undefined,
  });
  // InspectBase: snapshot of element meaning at each window path — built on every Inspect entry
  const [inspectBase, setInspectBase] = useState<SemanticWindowSelections>({});
  const [searchText, setSearchText] = useState("");
  const [searchState, setSearchState] = useState<SearchState>({ kind: "idle" });
  // BrowseFilter: direct snapshot of windowSelections while Browse is active
  const browseFilterRef = useRef<SemanticWindowSelections>({});
  // CommittedBrowseFilter: triggers meaning.query in BrowseView — set only on Cmd+Enter in Browse
  const [committedBrowseFilter, setCommittedBrowseFilter] =
    useState<CurrentSemanticFilter | null>(null);
  // Flag: target change came from Browse result — Inspect entry still seeds

  const modeRef = useRef(mode);
  const onInspectionModeChangeRef = useRef(onInspectionModeChange);
  const targetRef = useRef(target);
  const capabilityClientRef = useRef(capabilityClient);
  const windowSelectionsRef = useRef(windowSelections);
  const windowListRef = useRef(windowList);
  const inspectBaseRef = useRef(inspectBase);
  const selectedKindsRef = useRef(selectedKinds);
  const activeProjectionRef = useRef(activeProjection);
  const contextFilterModeRef = useRef(contextFilterMode);
  useEffect(() => { modeRef.current = mode; }, [mode]);
  useEffect(() => { onInspectionModeChangeRef.current = onInspectionModeChange; }, [onInspectionModeChange]);
  useEffect(() => { onInspectionModeChangeRef.current(mode); }, [mode]);
  useEffect(() => { targetRef.current = target; }, [target]);
  useEffect(() => { capabilityClientRef.current = capabilityClient; }, [capabilityClient]);
  useEffect(() => { windowSelectionsRef.current = windowSelections; }, [windowSelections]);
  useEffect(() => { windowListRef.current = windowList; }, [windowList]);
  useEffect(() => { inspectBaseRef.current = inspectBase; }, [inspectBase]);
  useEffect(() => { selectedKindsRef.current = selectedKinds; }, [selectedKinds]);
  useEffect(() => { activeProjectionRef.current = activeProjection; }, [activeProjection]);
  useEffect(() => { contextFilterModeRef.current = contextFilterMode; }, [contextFilterMode]);

  // While Browse is active: keep BrowseFilter in sync with windowSelections (direct snapshot)
  useEffect(() => {
    if (mode === "browse") {
      browseFilterRef.current = windowSelections;
    }
  }, [mode, windowSelections]);

  // Build live CurrentSemanticFilter from controlled selections + windowList
  const liveSemanticFilter = useMemo((): CurrentSemanticFilter | null => {
    const windows = windowList
      .map((w) => {
        const sel = windowSelections[w.id];
        return {
          windowId: w.id,
          activePath: w.path,
          selectedKeys: sel?.selectedKeys ?? [],
          combinator: sel?.combinator ?? "OR",
          valueLevel: w.valueLevel,
        };
      })
      .filter((w) => w.activePath.length > 0);
    const context =
      contextFilterMode !== "off" && target?.context
        ? { ctxId: target.context, mode: contextFilterMode }
        : undefined;
    if (windows.length === 0 && selectedKinds.size === 0 && !context) return null;
    return { windows, kinds: Array.from(selectedKinds), context };
  }, [windowSelections, windowList, selectedKinds, contextFilterMode, target]);

  // Switch to inspect mode when a new target is set
  useEffect(() => {
    if (target) setMode("inspect");
  }, [target]);

  // Read element when target changes
  useEffect(() => {
    const sequence = ++requestSequence.current;

    if (!target) {
      setReadState({ target: undefined, capability: undefined, result: undefined, loading: false, error: undefined });
      setInspectBase({});
      return;
    }

    const capability = getInspectionReadCapability(target);
    setReadState({ target, capability, result: undefined, loading: true, error: undefined });
    setInspectBase({});

    void readInspectionTarget(capabilityClient, target).then(
      (response) => {
        if (sequence !== requestSequence.current) return;
        const result = getInspectionResult(target, response.payload);

        if (!response.ok || !result || hasFailedItem(result)) {
          setReadState({
            target, capability, result: undefined, loading: false,
            error: response.error?.message ?? getItemError(result) ?? "Unable to read selected element Meaning.",
          });
          return;
        }

        setReadState({ target, capability, result, loading: false, error: undefined });
      },
      (error: unknown) => {
        if (sequence !== requestSequence.current) return;
        setReadState({
          target, capability, result: undefined, loading: false,
          error: error instanceof Error ? error.message : "Unable to read selected element Meaning.",
        });
      }
    );
  }, [capabilityClient, target]);

  // When read succeeds, build InspectBase using current windowList and seed windowSelections
  // Depends only on target (via readState.result identity) — not on windowList changes
  useEffect(() => {
    const result = readState.result;
    if (!result) return;

    const currentWindowList = windowListRef.current;
    const base: SemanticWindowSelections = {};
    for (const w of currentWindowList) {
      if (w.path.length === 0) continue;
      const value = resolvePathValue(result, w.path);
      const keys = extractStringKeys(value);
      if (keys.length > 0) {
        base[w.id] = { selectedKeys: keys, combinator: "OR" };
      }
    }
    setInspectBase(base);
    onInspectSeed(base);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readState.result]);

  // Browse result selected: switch target (triggers the read effect above)
  const handleBrowseTargetSelect = useCallback((browseTarget: InspectionTarget) => {
    onInspectionTargetChange(browseTarget);
    setMode("inspect");
  }, [onInspectionTargetChange]);

  // Entering Browse: send saved BrowseFilter for reconciliation
  const handleEnterBrowse = useCallback(() => {
    onBrowseRestore(browseFilterRef.current);
    setMode("browse");
  }, [onBrowseRestore]);

  const handleRefreshInspection = useCallback(() => {
    const currentTarget = targetRef.current;
    if (!currentTarget) return;
    void updateMeaningProjectionForTarget(
      capabilityClientRef.current,
      currentTarget
    ).finally(() => {
      onInspectionTargetChange({ ...currentTarget });
    });
  }, [onInspectionTargetChange]);

  // Cmd+Enter: Browse → commit filter and trigger query; Inspect → patch meaning and re-fetch
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key !== "Enter") return;
      if (modeRef.current !== "browse" && modeRef.current !== "inspect") return;
      e.preventDefault();

      if (modeRef.current === "browse") {
        // Commit live filter into committedBrowseFilter to trigger BrowseView query
        const currentWindowList = windowListRef.current;
        const currentSelections = windowSelectionsRef.current;
        const currentKinds = selectedKindsRef.current;
        const windows = currentWindowList
          .map((w) => {
            const sel = currentSelections[w.id];
            return {
              windowId: w.id,
              activePath: w.path,
              selectedKeys: sel?.selectedKeys ?? [],
              combinator: sel?.combinator ?? ("AND" as const),
              valueLevel: w.valueLevel,
            };
          })
          .filter((w) => w.selectedKeys.length > 0 && w.activePath.length > 0);
        const currentContextFilterMode = contextFilterModeRef.current;
        const currentTargetContext = targetRef.current?.context;
        const context =
          currentContextFilterMode !== "off" && currentTargetContext
            ? { ctxId: currentTargetContext, mode: currentContextFilterMode }
            : undefined;
        const committed: CurrentSemanticFilter | null =
          windows.length === 0 && currentKinds.size === 0 && !context
            ? null
            : { windows, kinds: Array.from(currentKinds), context };
        setCommittedBrowseFilter(committed);
        return;
      }

      if (modeRef.current === "inspect") {
        const currentTarget = targetRef.current;
        if (!currentTarget) return;
        if (activeProjectionRef.current !== "semantic") return;
        const currentWindowList = windowListRef.current;
        const currentSelections = windowSelectionsRef.current;
        const currentBase = inspectBaseRef.current;

        const semanticPatches = currentWindowList
          .map((w) => {
            const selectedKeys = currentSelections[w.id]?.selectedKeys ?? [];
            const baseKeys = currentBase[w.id]?.selectedKeys ?? [];
            return {
              path: w.path,
              valueLevel: w.valueLevel,
              add: selectedKeys.filter((key) => !baseKeys.includes(key)),
              remove: baseKeys.filter((key) => !selectedKeys.includes(key)),
            };
          })
          .filter((patch) => patch.valueLevel && patch.path.length >= 2 && (patch.add.length > 0 || patch.remove.length > 0));

        if (semanticPatches.length === 0) return;
        void patchInspectionTargetMeaning(
          capabilityClientRef.current,
          currentTarget,
          semanticPatches
        ).then(() => updateMeaningProjectionForTarget(capabilityClientRef.current, currentTarget)).then(() => {
          // re-fetch via target identity change — read effect rebuilds InspectBase and re-seeds
          onInspectionTargetChange({ ...currentTarget });
        });
      }
    };

    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onInspectionTargetChange]);

  const sourceFilePath = resolveInspectionSourcePath(contextPath, target);

  return (
    <aside
      aria-label="Brique Editor inspection"
      data-space="inspection"
      style={styles.root}
    >
      <header style={styles.header}>
        <InspectionDisplayControl
          displayMode={displayMode}
          onDisplayModeChange={onDisplayModeChange}
        />
        {displayMode === "expanded" && (
          <InspectionModeControl
            canOpenSource={Boolean(hostBridge?.openLocalPath && sourceFilePath)}
            mode={mode}
            onModeChange={(nextMode) => {
              if (nextMode === "browse" && mode !== "browse") {
                handleEnterBrowse();
              } else {
                setMode(nextMode);
              }
            }}
            onOpenSource={() => {
              if (!hostBridge?.openLocalPath || !sourceFilePath) return;
              void hostBridge.openLocalPath({ path: sourceFilePath, reveal: false });
            }}
          />
        )}
      </header>
      {displayMode === "expanded" && (
        <div style={styles.body}>
          {mode === "inspect" && (
            <InspectView
              readState={readState}
              semanticFilter={activeProjection === "semantic" ? liveSemanticFilter : null}
              inspectBase={activeProjection === "semantic" ? inspectBase : {}}
              onRefresh={handleRefreshInspection}
              onActivateContext={onActivateContext}
              onOpenInNewTab={onOpenInNewTab}
            />
          )}
          {mode === "browse" && (
            <BrowseView
              semanticFilter={committedBrowseFilter}
              onTargetSelect={handleBrowseTargetSelect}
              onActivateContext={onActivateContext}
              onOpenInNewTab={onOpenInNewTab}
            />
          )}
          {mode === "search" && (
            <SearchView
              onTargetSelect={handleBrowseTargetSelect}
              onActivateContext={onActivateContext}
              text={searchText}
              onTextChange={setSearchText}
              searchState={searchState}
              onSearchStateChange={setSearchState}
              onOpenInNewTab={onOpenInNewTab}
            />
          )}
        </div>
      )}
    </aside>
  );
}

function resolveInspectionSourcePath(
  contextPath: string | undefined,
  target: InspectionTarget | undefined
): string | undefined {
  if (!contextPath || !target) return undefined;

  const rootDir = toPosixPath(contextPath).replace(/\/context\.json$/, "");
  const contextSuffix = target.context.replace(/^\/root\/?/, "");
  const contextDir = contextSuffix ? `${rootDir}/${contextSuffix}` : rootDir;

  if (target.elementKind === "context") return `${contextDir}/context.json`;

  const sourcePath = target.sourcePath;
  if (!sourcePath) return undefined;
  const resolvedPath = resolvePathFromRootOrContext(rootDir, contextDir, sourcePath);
  if (target.elementKind === "matter" && !resolvedPath.endsWith(".matter.json")) {
    return `${resolvedPath}.matter.json`;
  }
  if (target.elementKind === "structure" && !resolvedPath.endsWith(".json")) {
    return `${resolvedPath}.json`;
  }
  return resolvedPath;
}

function resolvePathFromRootOrContext(
  rootDir: string,
  contextDir: string,
  sourcePath: string
): string {
  if (sourcePath.startsWith("/root/")) {
    return `${rootDir}/${sourcePath.replace(/^\/root\/?/, "")}`;
  }
  if (sourcePath.startsWith("/")) return `${rootDir}${sourcePath}`;
  return `${contextDir}/${sourcePath}`;
}

type CapabilityClient = ReturnType<typeof createCapabilityClient>;
type InspectionCapability = NonNullable<InspectionReadState["capability"]>;
type InspectionResponse = {
  ok: boolean;
  payload: unknown;
  error: { message?: string } | undefined;
};

type SemanticPatchSelection = {
  path: VocabularyPath;
  add: string[];
  remove: string[];
};

function getInspectionReadCapability(target: InspectionTarget): InspectionCapability {
  if (target.elementKind === "matter") return "matter.read";
  if (target.elementKind === "structure") return "structure.read";
  return "read.meaning";
}

async function readInspectionTarget(
  capabilityClient: CapabilityClient,
  target: InspectionTarget
): Promise<InspectionResponse> {
  if (target.elementKind === "matter") {
    return capabilityClient.matter.read(target.context, {
      matter_id: target.elementName,
      read_mode: "meaning|functional|brique",
    });
  }

  if (target.elementKind === "structure") {
    return capabilityClient.structure.read(target.context, {
      structure_id: target.elementName,
      want_meaning: true,
      want_functional: true,
      want_brique: true,
    });
  }

  return capabilityClient.read.meaning(target.context, {
    input: [
      {
        element_kind: target.elementKind,
        element_name: target.elementName,
        sections: ["brique", "objective", "subjective", "functional"],
      },
    ],
  });
}

async function patchInspectionTargetMeaning(
  capabilityClient: CapabilityClient,
  target: InspectionTarget,
  selections: SemanticPatchSelection[]
): Promise<unknown> {
  const semantic_patch = buildSemanticValuePatch(selections);
  if (target.elementKind === "matter") {
    return capabilityClient.matter.write(target.context, {
      matter_id: target.elementName,
      semantic_patch,
    });
  }

  if (target.elementKind === "structure") {
    return capabilityClient.structure.patch(target.context, {
      structure_id: target.elementName,
      semantic_patch,
    });
  }

  return capabilityClient.edit.patchMeaning(target.context, {
    items: [{
      ctx_id: target.context,
      element_kind: target.elementKind,
      element_name: target.elementName,
      semantic_patch,
    }],
  });
}

async function updateMeaningProjectionForTarget(
  capabilityClient: CapabilityClient,
  target: InspectionTarget
): Promise<unknown> {
  return capabilityClient.meaning.update(ROOT_CONTEXT, {
    elements: [{
      ctx_id: target.context,
      element_kind: target.elementKind,
      name: target.elementName,
    }],
  });
}

function buildSemanticValuePatch(selections: SemanticPatchSelection[]): {
  operations: Array<{
    op: "add" | "remove";
    path: Array<string | number>;
    value: string;
  }>;
} {
	const operations: Array<{
		op: "add" | "remove";
		path: Array<string | number>;
		value: string;
	}> = [];
  for (const selection of selections) {
		for (const value of selection.add) operations.push({ op: "add", path: selection.path, value });
		for (const value of selection.remove) operations.push({ op: "remove", path: selection.path, value });
  }
	return { operations };
}

function getInspectionResult(
  target: InspectionTarget,
  payload: unknown
): InspectionReadResult | undefined {
  const payloadRecord = getRecord(payload);
  if (!payloadRecord) return undefined;
  if (target.elementKind === "matter" || target.elementKind === "structure") {
    return pickSemanticSections(payloadRecord);
  }
  const item = (payload as { result?: Record<string, unknown>[] }).result?.[0];
  if (!item || item.ok === false) return item as InspectionReadResult | undefined;
  return pickSemanticSections(item);
}

function hasFailedItem(result: InspectionReadResult): boolean {
  return "ok" in result && result.ok === false;
}

function pickSemanticSections(item: Record<string, unknown>): InspectionReadResult {
  const descriptor =
    getRecord(item.descriptor) ??
    getRecord(item.desc) ??
    getRecord(item.meaning);
  const source = descriptor ?? item;

  return {
    ...(item.brique !== undefined || source.brique !== undefined
      ? { brique: item.brique ?? source.brique }
      : {}),
    ...(source.objective !== undefined ? { objective: source.objective } : {}),
    ...(source.subjective !== undefined ? { subjective: source.subjective } : {}),
    ...(item.functional !== undefined || source.functional !== undefined
      ? { functional: item.functional ?? source.functional }
      : {}),
  };
}

function getRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null
    ? value as Record<string, unknown>
    : undefined;
}

function resolvePathValue(result: InspectionReadResult, path: string[]): unknown {
  let current: unknown = result;
  for (const segment of path) {
    if (typeof current !== "object" || current === null) return undefined;
    current = (current as Record<string, unknown>)[segment];
  }
  return current;
}

function extractStringKeys(value: unknown): string[] {
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) return value.filter((v): v is string => typeof v === "string");
  if (typeof value === "object" && value !== null) return Object.keys(value);
  return [];
}

function getItemError(result: Record<string, unknown> | undefined): string | undefined {
  const error = result?.error;
  if (typeof error === "string") return error;
  if (typeof error !== "object" || error === null) return undefined;
  const message = (error as Record<string, unknown>).message;
  return typeof message === "string" ? message : undefined;
}

const styles: Record<string, React.CSSProperties> = {
  root: {
    position: "relative",
    display: "flex",
    flexDirection: "column",
    width: "100%",
    height: "100%",
    minWidth: 0,
    minHeight: 0,
    boxSizing: "border-box",
    overflow: "hidden",
    background: "var(--syn-inspection-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
  },
  header: {
    display: "flex",
    alignItems: "center",
    flex: "0 0 40px",
    minWidth: 0,
    gap: "8px",
    padding: "8px",
    boxSizing: "border-box",
    background: "var(--syn-inspection-panel-bg)",
  },
  body: {
    display: "flex",
    flex: 1,
    minWidth: 0,
    minHeight: 0,
    overflow: "hidden",
  },
};
