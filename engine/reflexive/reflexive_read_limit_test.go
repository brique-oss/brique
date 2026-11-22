package reflexive

import (
	"encoding/json"
	"testing"
)

func navTree(width, depth int) NavItem {
	node := NavItem{Path: "p", Kind: "dir"}
	if depth == 0 {
		return node
	}
	for i := 0; i < width; i++ {
		node.Children = append(node.Children, navTree(width, depth-1))
	}
	node.ChildrenTotal = width
	return node
}

func TestLimitNavNodesKeepsTheNearestLevelsWhole(t *testing.T) {
	tree := navTree(5, 3) // 1 + 5 + 25 + 125 nodes
	total := countNavNodes(tree)
	if total != 156 {
		t.Fatalf("tree of %d nodes", total)
	}
	limitNavNodes(&tree, 40)
	if got := countNavNodes(tree); got != 40 {
		t.Fatalf("kept %d nodes, want 40", got)
	}
	if len(tree.Children) != 5 {
		t.Fatalf("the first level must stay whole: %d children", len(tree.Children))
	}
	for _, child := range tree.Children {
		if len(child.Children) != 5 {
			t.Fatalf("the second level must stay whole: %d children", len(child.Children))
		}
	}
	cut := false
	for _, child := range tree.Children {
		for _, grand := range child.Children {
			if grand.ChildrenTrunc {
				cut = true
				if grand.ChildrenTotal != 5 {
					t.Fatalf("a cut node keeps the real children_total: %d", grand.ChildrenTotal)
				}
			}
		}
	}
	if !cut {
		t.Fatal("a node that lost children must say so")
	}
	small := navTree(2, 2)
	limitNavNodes(&small, 150)
	if countNavNodes(small) != 7 {
		t.Fatal("a tree under the limit is left as it is")
	}
}

func TestCompactNavKeepsWhatTheFullFormSays(t *testing.T) {
	tree := NavItem{Path: "", Kind: "context", Name: "agentic", ChildrenTotal: 3, Children: []NavItem{
		{Path: "capacity", Kind: "dir", Name: "capacity", ChildrenTotal: 3, ChildrenTrunc: true, Children: []NavItem{
			{Path: "capacity/a.json", Kind: "capacity", Name: "a.json"},
			{Path: "capacity/b.json", Kind: "capacity", Name: "b.json"},
		}},
		{Path: "code", Kind: "dir", Name: "code", ChildrenTotal: 1, Children: []NavItem{
			{Path: "code/core", Kind: "dir", Name: "core", ChildrenTotal: 2, Children: []NavItem{
				{Path: "code/core/x.py", Kind: "file", Name: "x.py"},
				{Path: "code/core/sub", Kind: "dir", Name: "sub"},
			}},
		}},
		{Path: "context.json", Kind: "context_file", Name: "context.json"},
	}}
	data, err := json.Marshal(compactOf(tree))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"name":"agentic","kind":"context","total":3,"leaf_kind":"context_file","children":[` +
		`{"name":"capacity","kind":"dir","total":3,"truncated":true,"leaf_kind":"capacity","children":["a.json","b.json"]},` +
		`{"name":"code","kind":"dir","total":1,"children":[{"name":"core","kind":"dir","total":2,"leaf_kind":"file","children":["x.py",{"name":"sub","kind":"dir"}]}]},` +
		`"context.json"]}`
	if string(data) != want {
		t.Fatalf("compact form:\n got %s\nwant %s", data, want)
	}
	full, _ := json.Marshal(tree)
	if len(data)*10 > len(full)*6 {
		t.Fatalf("the compact form is not much smaller: %d against %d", len(data), len(full))
	}
}
