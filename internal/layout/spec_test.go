package layout

import (
	"encoding/json"
	"testing"
)

func TestMasterStackValidation(t *testing.T) {
	spec := NewMasterStack(50)
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid master-stack spec failed validation: %v", err)
	}
}

func TestGridValidation(t *testing.T) {
	spec := NewGrid()
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid grid spec failed validation: %v", err)
	}
}

func TestColumnsValidation(t *testing.T) {
	spec := NewColumns([]float64{30, 40, 30})
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid columns spec failed validation: %v", err)
	}
}

func TestDualTabbedValidation(t *testing.T) {
	spec := NewDualTabbed(50)
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid dual-tabbed spec failed validation: %v", err)
	}
}

func TestSpiralValidation(t *testing.T) {
	spec := NewSpiral()
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid spiral spec failed validation: %v", err)
	}
}

func TestAllBuiltinsValidate(t *testing.T) {
	for name, spec := range BuiltinSpecs() {
		if err := spec.Validate(); err != nil {
			t.Errorf("builtin %q failed validation: %v", name, err)
		}
	}
}

func TestMasterStackJSONRoundtrip(t *testing.T) {
	original := NewMasterStack(60)
	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored LayoutSpec
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Name != original.Name {
		t.Errorf("name mismatch: got %q, want %q", restored.Name, original.Name)
	}
	if restored.Root.Kind != NodeContainer {
		t.Errorf("root kind: got %q, want %q", restored.Root.Kind, NodeContainer)
	}
	if restored.Root.Layout != LayoutSplitH {
		t.Errorf("root layout: got %q, want %q", restored.Root.Layout, LayoutSplitH)
	}
	if len(restored.Root.Children) != 2 {
		t.Fatalf("root children: got %d, want 2", len(restored.Root.Children))
	}
	if restored.Root.Children[0].Size != 60 {
		t.Errorf("master size: got %g, want 60", restored.Root.Children[0].Size)
	}
	if restored.Root.Children[1].Size != 40 {
		t.Errorf("stack size: got %g, want 40", restored.Root.Children[1].Size)
	}
	if restored.Policy.MasterSlot != "master" {
		t.Errorf("master slot: got %q, want %q", restored.Policy.MasterSlot, "master")
	}
	if restored.Policy.OverflowSlot != "stack" {
		t.Errorf("overflow slot: got %q, want %q", restored.Policy.OverflowSlot, "stack")
	}
}

func TestDualTabbedJSONRoundtrip(t *testing.T) {
	original := NewDualTabbed(65)
	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored LayoutSpec
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Name != "dual-tabbed" {
		t.Errorf("name: got %q, want %q", restored.Name, "dual-tabbed")
	}

	// Verify the nested tabbed structure
	root := restored.Root
	if root.Layout != LayoutSplitH {
		t.Errorf("root layout: got %q, want %q", root.Layout, LayoutSplitH)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root children: got %d, want 2", len(root.Children))
	}

	primary := root.Children[0].Node
	if primary.Layout != LayoutTabbed {
		t.Errorf("primary layout: got %q, want %q", primary.Layout, LayoutTabbed)
	}

	secondary := root.Children[1].Node
	if secondary.Layout != LayoutTabbed {
		t.Errorf("secondary layout: got %q, want %q", secondary.Layout, LayoutTabbed)
	}
}

func TestSlotIDs(t *testing.T) {
	spec := NewMasterStack(50)
	ids := spec.SlotIDs()
	if len(ids) != 2 {
		t.Fatalf("slot count: got %d, want 2", len(ids))
	}
	if ids[0] != "master" {
		t.Errorf("first slot: got %q, want %q", ids[0], "master")
	}
	if ids[1] != "stack" {
		t.Errorf("second slot: got %q, want %q", ids[1], "stack")
	}
}

func TestGridSlotIDs(t *testing.T) {
	spec := NewGrid()
	ids := spec.SlotIDs()
	if len(ids) != 4 {
		t.Fatalf("slot count: got %d, want 4", len(ids))
	}
	expected := []string{"top-left", "top-right", "bottom-left", "bottom-right"}
	for i, want := range expected {
		if ids[i] != want {
			t.Errorf("slot %d: got %q, want %q", i, ids[i], want)
		}
	}
}

func TestFindNode(t *testing.T) {
	spec := NewGrid()

	node := spec.FindNode("top-left")
	if node == nil {
		t.Fatal("expected to find top-left node")
	}
	if node.Kind != NodeSlot {
		t.Errorf("top-left kind: got %q, want %q", node.Kind, NodeSlot)
	}
	if node.Role != "cell" {
		t.Errorf("top-left role: got %q, want %q", node.Role, "cell")
	}

	node = spec.FindNode("top-row")
	if node == nil {
		t.Fatal("expected to find top-row node")
	}
	if node.Kind != NodeContainer {
		t.Errorf("top-row kind: got %q, want %q", node.Kind, NodeContainer)
	}

	node = spec.FindNode("nonexistent")
	if node != nil {
		t.Error("expected nil for nonexistent node")
	}
}

// --- Validation error cases ---

func TestValidationDuplicateIDs(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{
			ID:     "root",
			Kind:   NodeContainer,
			Layout: LayoutSplitH,
			Children: []Child{
				{Size: 50, Node: Node{ID: "dupe", Kind: NodeSlot, Role: "a"}},
				{Size: 50, Node: Node{ID: "dupe", Kind: NodeSlot, Role: "b"}},
			},
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for duplicate IDs")
	}
}

func TestValidationEmptyName(t *testing.T) {
	spec := &LayoutSpec{
		Root: Node{ID: "root", Kind: NodeSlot, Role: "all"},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty name")
	}
}

func TestValidationContainerWithNoChildren(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{ID: "root", Kind: NodeContainer, Layout: LayoutSplitH},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for container with no children")
	}
}

func TestValidationSlotWithChildren(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{
			ID:   "root",
			Kind: NodeSlot,
			Role: "all",
			Children: []Child{
				{Size: 100, Node: Node{ID: "child", Kind: NodeSlot, Role: "x"}},
			},
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for slot with children")
	}
}

func TestValidationBadSizeSum(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{
			ID:     "root",
			Kind:   NodeContainer,
			Layout: LayoutSplitH,
			Children: []Child{
				{Size: 30, Node: Node{ID: "a", Kind: NodeSlot, Role: "a"}},
				{Size: 30, Node: Node{ID: "b", Kind: NodeSlot, Role: "b"}},
			},
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for sizes summing to 60")
	}
}

func TestValidationBadPolicyRef(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{ID: "root", Kind: NodeSlot, Role: "all"},
		Policy: LayoutPolicy{
			DefaultSlot: "nonexistent",
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for bad policy ref")
	}
}

func TestValidationContainerWithRole(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{
			ID:     "root",
			Kind:   NodeContainer,
			Layout: LayoutSplitH,
			Role:   "should-not-be-here",
			Children: []Child{
				{Size: 100, Node: Node{ID: "a", Kind: NodeSlot, Role: "a"}},
			},
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for container with role")
	}
}

func TestValidationSlotWithLayout(t *testing.T) {
	spec := &LayoutSpec{
		Name: "bad",
		Root: Node{
			ID:     "root",
			Kind:   NodeSlot,
			Layout: LayoutTabbed,
			Role:   "all",
		},
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected validation error for slot with layout")
	}
}
