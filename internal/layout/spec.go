// Package layout defines the data model for declarative, serializable workspace layouts.
//
// The model has three layers:
//
//   - Structure: a tree of container and slot nodes describing the spatial shape
//   - Policy: rules governing window placement, overflow, and master semantics
//   - Runtime: interactive operations (swap-master, rotate, etc.) handled by the engine
//
// This separation means specs are pure data — they can be captured from a live
// sway tree, stored as JSON/TOML, edited by agents, and restored later.
package layout

import (
	"encoding/json"
	"errors"
	"fmt"
)

// NodeKind distinguishes container nodes from slot (leaf) nodes.
type NodeKind string

const (
	// NodeContainer is a node that holds child nodes and arranges them with a layout.
	NodeContainer NodeKind = "container"
	// NodeSlot is a leaf node that receives windows.
	NodeSlot NodeKind = "slot"
)

// ContainerLayout determines how a container arranges its children.
type ContainerLayout string

const (
	LayoutSplitH   ContainerLayout = "splith"
	LayoutSplitV   ContainerLayout = "splitv"
	LayoutTabbed   ContainerLayout = "tabbed"
	LayoutStacking ContainerLayout = "stacking"
)

// ValidContainerLayouts is the set of valid ContainerLayout values.
var ValidContainerLayouts = map[ContainerLayout]bool{
	LayoutSplitH:   true,
	LayoutSplitV:   true,
	LayoutTabbed:   true,
	LayoutStacking: true,
}

// Node is a single element in the layout tree. It is either a container
// (with children and a layout) or a slot (a leaf that holds windows).
//
// Using a single type with a Kind discriminator keeps the recursive tree
// simple in Go. Validity is enforced by Validate().
type Node struct {
	// ID is a stable identifier for this node, used for snapshot keying.
	// Must be unique within a spec.
	ID string `json:"id"`

	// Kind is either "container" or "slot".
	Kind NodeKind `json:"kind"`

	// Layout determines how children are arranged. Container-only.
	Layout ContainerLayout `json:"layout,omitempty"`

	// Children are the child nodes with their size allocations. Container-only.
	Children []Child `json:"children,omitempty"`

	// Role is a semantic label for this slot (e.g., "master", "stack", "sidebar").
	// Slot-only. Used for policy references and human readability.
	Role string `json:"role,omitempty"`
}

// Child pairs a node with its size allocation within a parent container.
type Child struct {
	// Size is the percentage of the parent's space this child occupies (0-100).
	// Sizes across siblings should sum to 100.
	Size float64 `json:"size"`

	// Node is the child node.
	Node Node `json:"node"`
}

// LayoutSpec is a complete, named layout definition: structure + policy.
type LayoutSpec struct {
	// Name is the unique identifier for this layout (e.g., "master-stack").
	Name string `json:"name"`

	// Root is the top-level node of the layout tree.
	Root Node `json:"root"`

	// Policy governs window placement and behavior within this layout.
	Policy LayoutPolicy `json:"policy"`
}

// LayoutPolicy defines rules for window assignment and management within a layout.
// These are durable invariants — interactive operations (swap, rotate, maximize)
// are handled by the runtime engine, not stored in the policy.
type LayoutPolicy struct {
	// DefaultSlot is the slot ID where new windows are placed by default.
	DefaultSlot string `json:"default_slot,omitempty"`

	// MasterSlot is the slot ID designated as the "master" area.
	// Empty if the layout has no master concept.
	MasterSlot string `json:"master_slot,omitempty"`

	// MasterCount is how many windows the master area holds before
	// overflow goes to OverflowSlot. 0 means no limit.
	MasterCount int `json:"master_count,omitempty"`

	// OverflowSlot is where windows go when their target slot is full.
	OverflowSlot string `json:"overflow_slot,omitempty"`

	// VisibleLimit caps the number of visible windows in a slot before
	// the slot switches to tabbed/stacking. 0 means no limit.
	VisibleLimit int `json:"visible_limit,omitempty"`

	// VisibleLimitSlot is the slot to which VisibleLimit applies.
	// If empty, applies to OverflowSlot.
	VisibleLimitSlot string `json:"visible_limit_slot,omitempty"`

	// InsertionPoint controls where new windows appear within a slot.
	// "start" = prepend, "end" = append (default), "after-focused" = after current focus.
	InsertionPoint string `json:"insertion_point,omitempty"`
}

// Validate checks that a LayoutSpec is structurally valid.
func (s *LayoutSpec) Validate() error {
	if s.Name == "" {
		return errors.New("spec name is required")
	}

	ids := make(map[string]bool)
	slots := make(map[string]bool)
	if err := validateNode(&s.Root, ids, slots); err != nil {
		return fmt.Errorf("spec %q: %w", s.Name, err)
	}

	// Validate policy references
	if s.Policy.DefaultSlot != "" && !slots[s.Policy.DefaultSlot] {
		return fmt.Errorf("spec %q: policy.default_slot %q references unknown slot", s.Name, s.Policy.DefaultSlot)
	}
	if s.Policy.MasterSlot != "" && !slots[s.Policy.MasterSlot] {
		return fmt.Errorf("spec %q: policy.master_slot %q references unknown slot", s.Name, s.Policy.MasterSlot)
	}
	if s.Policy.OverflowSlot != "" && !slots[s.Policy.OverflowSlot] {
		return fmt.Errorf("spec %q: policy.overflow_slot %q references unknown slot", s.Name, s.Policy.OverflowSlot)
	}
	if s.Policy.VisibleLimitSlot != "" && !slots[s.Policy.VisibleLimitSlot] {
		return fmt.Errorf("spec %q: policy.visible_limit_slot %q references unknown slot", s.Name, s.Policy.VisibleLimitSlot)
	}

	return nil
}

func validateNode(n *Node, ids, slots map[string]bool) error {
	if n.ID == "" {
		return errors.New("node ID is required")
	}
	if ids[n.ID] {
		return fmt.Errorf("duplicate node ID %q", n.ID)
	}
	ids[n.ID] = true

	switch n.Kind {
	case NodeContainer:
		if !ValidContainerLayouts[n.Layout] {
			return fmt.Errorf("container %q has invalid layout %q", n.ID, n.Layout)
		}
		if len(n.Children) == 0 {
			return fmt.Errorf("container %q has no children", n.ID)
		}
		if n.Role != "" {
			return fmt.Errorf("container %q should not have a role (role is slot-only)", n.ID)
		}
		var totalSize float64
		for i := range n.Children {
			c := &n.Children[i]
			if c.Size <= 0 || c.Size > 100 {
				return fmt.Errorf("child %d of container %q has invalid size %g (must be 0-100)", i, n.ID, c.Size)
			}
			totalSize += c.Size
			if err := validateNode(&c.Node, ids, slots); err != nil {
				return err
			}
		}
		// Allow small floating-point tolerance
		if totalSize < 99.0 || totalSize > 101.0 {
			return fmt.Errorf("children of container %q have sizes summing to %g, expected ~100", n.ID, totalSize)
		}

	case NodeSlot:
		if len(n.Children) > 0 {
			return fmt.Errorf("slot %q should not have children", n.ID)
		}
		if n.Layout != "" {
			return fmt.Errorf("slot %q should not have a layout (layout is container-only)", n.ID)
		}
		slots[n.ID] = true

	default:
		return fmt.Errorf("node %q has unknown kind %q", n.ID, n.Kind)
	}

	return nil
}

// SlotIDs returns the IDs of all slot nodes in the spec, in tree order.
func (s *LayoutSpec) SlotIDs() []string {
	var ids []string
	collectSlots(&s.Root, &ids)
	return ids
}

func collectSlots(n *Node, ids *[]string) {
	if n.Kind == NodeSlot {
		*ids = append(*ids, n.ID)
		return
	}
	for i := range n.Children {
		collectSlots(&n.Children[i].Node, ids)
	}
}

// FindNode returns the node with the given ID, or nil if not found.
func (s *LayoutSpec) FindNode(id string) *Node {
	return findNode(&s.Root, id)
}

func findNode(n *Node, id string) *Node {
	if n.ID == id {
		return n
	}
	for i := range n.Children {
		if found := findNode(&n.Children[i].Node, id); found != nil {
			return found
		}
	}
	return nil
}

// MarshalJSON produces the JSON representation of a LayoutSpec.
func (s *LayoutSpec) MarshalJSON() ([]byte, error) {
	type Alias LayoutSpec
	return json.Marshal((*Alias)(s))
}

// UnmarshalJSON parses a LayoutSpec from JSON and validates it.
func (s *LayoutSpec) UnmarshalJSON(data []byte) error {
	type Alias LayoutSpec
	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*s = LayoutSpec(alias)
	return s.Validate()
}
