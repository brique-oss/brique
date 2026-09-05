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

import { useMemo } from "react";
import { useBriqueProjection } from "../../../React_Substrate_Adapter/hooks.js";
import { meaningElementKeyId } from "../../../Brique_Substrate/resolver/index.js";
import type { FlowProjectionModel } from "../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import {
  flowDataGet,
  isFlowDataObject,
} from "../../../Brique_Substrate/projection/Flow/flowData.js";
import type {
  FlowDataObject,
  FlowDataValue,
} from "../../../Brique_Substrate/projection/Flow/flowProjectionModel.js";
import type { FlowCapacityTarget } from "../contracts.js";
import { FlowProjectionFeedback } from "./ProjectionFeedback/FlowProjectionFeedback.js";
import { FlowSectionFlow } from "./SectionFlow/FlowSectionFlow.js";
import type { FlowBriqueRefCallbacks } from "./SectionFlow/FlowLayoutRenderer.js";
import type {
  FlowCapacitySummary,
  FlowProjectionFeedbackState,
} from "./contracts.js";

export type ProjectionFlowSpaceProps = {
  target: FlowCapacityTarget | undefined;
  onFlowCapacityTargetChange: (target: FlowCapacityTarget) => void;
  briqueRefCallbacks?: FlowBriqueRefCallbacks;
};

export function ProjectionFlowSpace({
  target,
  onFlowCapacityTargetChange,
  briqueRefCallbacks,
}: ProjectionFlowSpaceProps) {
  const key = useMemo(
    () =>
      target
        ? {
            context: target.context,
            kind: "read.meaning",
            id: meaningElementKeyId("capacity", target.capacityName),
          }
        : null,
    [target]
  );
  const projection = useBriqueProjection(key, "flow");
  const model =
    projection.data?.status === "ok" ? projection.data.model : undefined;
  const summary = getCapacitySummary(target, model);
  const feedback = getProjectionFeedback(target, projection, model);

  return (
    <section
      aria-label="Brique Editor flow projection"
      data-space="projection-flow"
      style={styles.root}
    >
      <FlowSectionFlow
        key={target ? `${target.context}:${target.capacityName}` : "empty"}
        model={model}
        onFlowCapacityTargetChange={onFlowCapacityTargetChange}
        briqueRefCallbacks={briqueRefCallbacks}
        onRefresh={() => void projection.reload()}
      />
      <FlowProjectionFeedback
        onRefresh={() => void projection.reload()}
        state={feedback}
      />
    </section>
  );
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
    background: "var(--syn-flow-bg)",
    color: "var(--syn-text-primary)",
    fontFamily: "monospace",
  },
};

function getCapacitySummary(
  target: FlowCapacityTarget | undefined,
  model: FlowProjectionModel | undefined
): FlowCapacitySummary | undefined {
  if (!target) return undefined;

  const rootData = model?.nodesById[model.rootId]?.data;
  const brique = readObject(flowDataGet(rootData, "brique"));
  const objective = readObject(flowDataGet(rootData, "objective"));

  return {
    name:
      readString(flowDataGet(brique, "cap_name")) ??
      readString(flowDataGet(objective, "name")) ??
      target.capacityName,
    context: target.context,
    objective: readString(flowDataGet(objective, "description")),
  };
}

function readObject(value: FlowDataValue | undefined): FlowDataObject | undefined {
  return isFlowDataObject(value) ? value : undefined;
}

function readString(value: FlowDataValue | undefined): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function getProjectionFeedback(
  target: FlowCapacityTarget | undefined,
  projection: ReturnType<typeof useBriqueProjection<"flow">>,
  model: FlowProjectionModel | undefined
): FlowProjectionFeedbackState {
  if (!target) {
    return {
      kind: "empty",
      message: "Select a capacity in Structure to display its flow.",
    };
  }
  if (projection.error) {
    return {
      kind: "error",
      message:
        projection.error instanceof Error
          ? projection.error.message
          : "Unable to construct the Flow projection.",
    };
  }
  if (projection.data?.status === "error") {
    return {
      kind: "error",
      message:
        projection.data.error.message ??
        "Unable to load the selected capacity Flow projection.",
    };
  }
  if (projection.data?.status === "absent") {
    return {
      kind: "error",
      message: "The selected capacity Flow projection is unavailable.",
    };
  }
  if (projection.loading && !model) {
    return { kind: "loading", message: "Loading Flow projection..." };
  }
  if (model && countSections(model) === 0) {
    return { kind: "empty", message: "This capacity contains no visible sections." };
  }
  return { kind: "hidden" };
}

function countSections(model: FlowProjectionModel): number {
  const root = model.nodesById[model.rootId];
  return root?.children.filter((child) => child.role === "section").length ?? 0;
}
