// The hierarchy of the conversations: continuities of one lineage (a succession changes the identifier, not the conversation) are one node,
// daughters hang under the node of their parent. Pure function of the headers of the continuities.

export type Header = {
  id: string;
  parent_id: string | null;
  children: string[];
  lineage_id: string;
  generation: number;
  predecessor_id?: string | null;
  successor_id?: string | null;
  status?: string;
  initial_intention: any;
  pilot_ref: { context: string; matter_id?: string; capacity?: string };
  created_at?: string;
};

export type LineageNode = {
  lineageId: string;
  head: string;                 // the continuity that receives the messages: the last generation
  generations: string[];        // identifiers, oldest first
  title: string;
  nature: "conversation" | "workflow";   // a Matter pilot converses, a Capacity pilot is a workflow
  children: LineageNode[];
};

const titleOf = (intention: any): string => (typeof intention === "string" ? intention : JSON.stringify(intention ?? ""));

export function buildTree(headers: Header[]): LineageNode[] {
  const byId = new Map(headers.map((h) => [h.id, h]));
  const lineages = new Map<string, Header[]>();
  for (const header of headers) {
    const group = lineages.get(header.lineage_id) ?? [];
    group.push(header);
    lineages.set(header.lineage_id, group);
  }
  const nodes = new Map<string, LineageNode>();
  for (const [lineageId, group] of lineages) {
    group.sort((a, b) => a.generation - b.generation);
    const first = group[0];
    nodes.set(lineageId, {
      lineageId, head: group[group.length - 1].id, generations: group.map((h) => h.id),
      title: titleOf(first.initial_intention), nature: first.pilot_ref?.capacity ? "workflow" : "conversation", children: [],
    });
  }
  const parentOf = (lineageId: string): string | null => {
    const parent = lineages.get(lineageId)![0].parent_id;
    const header = parent ? byId.get(parent) : undefined;
    return header ? header.lineage_id : null;       // a parent that no longer exists makes a root
  };
  const created = (lineageId: string) => lineages.get(lineageId)![0].created_at ?? "";
  const roots: LineageNode[] = [];
  for (const lineageId of [...nodes.keys()].sort((a, b) => created(a).localeCompare(created(b)))) {
    const parent = parentOf(lineageId);
    if (parent && parent !== lineageId && nodes.has(parent)) nodes.get(parent)!.children.push(nodes.get(lineageId)!);
    else roots.push(nodes.get(lineageId)!);
  }
  return roots.sort((a, b) => created(b.lineageId).localeCompare(created(a.lineageId)));   // the newest conversation first
}

export type Row = { node: LineageNode; depth: number };

export const flatten = (nodes: LineageNode[], depth = 0): Row[] => nodes.flatMap((node) => [{ node, depth }, ...flatten(node.children, depth + 1)]);

/** The rows shown: the descendants of a collapsed node are left out (the node itself stays). */
export function visibleRows(nodes: LineageNode[], collapsed: ReadonlySet<string>, depth = 0): Row[] {
  return nodes.flatMap((node) => [{ node, depth }, ...(collapsed.has(node.lineageId) ? [] : visibleRows(node.children, collapsed, depth + 1))]);
}

/** Every lineage under a node, at any depth. */
export function descendantsOf(node: LineageNode): LineageNode[] {
  return node.children.flatMap((child) => [child, ...descendantsOf(child)]);
}
