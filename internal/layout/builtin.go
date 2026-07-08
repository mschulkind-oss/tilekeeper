package layout

import "fmt"

// Built-in layout specs. These are the defaults — users can override
// or define their own in config.

// NewMasterStack returns the classic master-stack layout spec.
//
//	┌─────────┬──────────┐
//	│         │ Stack 1  │
//	│ Master  ├──────────┤
//	│         │ Stack 2  │
//	│         ├──────────┤
//	│         │ Stack 3  │
//	└─────────┴──────────┘
func NewMasterStack(masterWidth float64) *LayoutSpec {
	if masterWidth <= 0 || masterWidth >= 100 {
		masterWidth = 50
	}
	return &LayoutSpec{
		Name: "master-stack",
		Root: Node{
			ID:     "root",
			Kind:   NodeContainer,
			Layout: LayoutSplitH,
			Children: []Child{
				{
					Size: masterWidth,
					Node: Node{ID: "master", Kind: NodeSlot, Role: "master"},
				},
				{
					Size: 100 - masterWidth,
					Node: Node{ID: "stack", Kind: NodeSlot, Role: "stack"},
				},
			},
		},
		Policy: LayoutPolicy{
			DefaultSlot:      "master",
			MasterSlot:       "master",
			MasterCount:      1,
			OverflowSlot:     "stack",
			VisibleLimit:     3,
			VisibleLimitSlot: "stack",
			InsertionPoint:   "start",
		},
	}
}

// NewGrid returns a grid layout spec.
// Windows are balanced across quadrants.
//
//	┌─────────┬─────────┐
//	│    1    │    2    │
//	├─────────┼─────────┤
//	│    3    │    4    │
//	└─────────┴─────────┘
func NewGrid() *LayoutSpec {
	return &LayoutSpec{
		Name: "grid",
		Root: Node{
			ID:     "root",
			Kind:   NodeContainer,
			Layout: LayoutSplitV,
			Children: []Child{
				{
					Size: 50,
					Node: Node{
						ID:     "top-row",
						Kind:   NodeContainer,
						Layout: LayoutSplitH,
						Children: []Child{
							{Size: 50, Node: Node{ID: "top-left", Kind: NodeSlot, Role: "cell"}},
							{Size: 50, Node: Node{ID: "top-right", Kind: NodeSlot, Role: "cell"}},
						},
					},
				},
				{
					Size: 50,
					Node: Node{
						ID:     "bottom-row",
						Kind:   NodeContainer,
						Layout: LayoutSplitH,
						Children: []Child{
							{Size: 50, Node: Node{ID: "bottom-left", Kind: NodeSlot, Role: "cell"}},
							{Size: 50, Node: Node{ID: "bottom-right", Kind: NodeSlot, Role: "cell"}},
						},
					},
				},
			},
		},
		Policy: LayoutPolicy{
			DefaultSlot:    "top-left",
			InsertionPoint: "end",
		},
	}
}

// NewColumns returns a multi-column layout spec with configurable ratios.
//
//	┌────┬──────────┬────┐
//	│    │          │    │
//	│ 30%│   40%    │30% │
//	│    │          │    │
//	└────┴──────────┴────┘
func NewColumns(ratios []float64) *LayoutSpec {
	if len(ratios) == 0 {
		ratios = []float64{50, 50}
	}

	children := make([]Child, len(ratios))
	defaultSlot := ""
	for i, ratio := range ratios {
		id := fmt.Sprintf("col-%d", i)
		if i == 0 {
			defaultSlot = id
		}
		children[i] = Child{
			Size: ratio,
			Node: Node{ID: id, Kind: NodeSlot, Role: "column"},
		}
	}

	return &LayoutSpec{
		Name: "columns",
		Root: Node{
			ID:       "root",
			Kind:     NodeContainer,
			Layout:   LayoutSplitH,
			Children: children,
		},
		Policy: LayoutPolicy{
			DefaultSlot:    defaultSlot,
			InsertionPoint: "end",
		},
	}
}

// NewDualTabbed returns a dual-tabbed layout: two side-by-side tabbed containers.
// Each side holds a group of windows in tabs; navigation between groups is
// horizontal, within groups is via tab switching.
//
//	┌──────────────┬──────────────┐
//	│ [tab1] tab2  │ [tab3] tab4  │
//	│              │              │
//	│  primary     │  secondary   │
//	│  group       │  group       │
//	│              │              │
//	└──────────────┴──────────────┘
func NewDualTabbed(primaryWidth float64) *LayoutSpec {
	if primaryWidth <= 0 || primaryWidth >= 100 {
		primaryWidth = 50
	}
	return &LayoutSpec{
		Name: "dual-tabbed",
		Root: Node{
			ID:     "root",
			Kind:   NodeContainer,
			Layout: LayoutSplitH,
			Children: []Child{
				{
					Size: primaryWidth,
					Node: Node{
						ID:     "primary-tabs",
						Kind:   NodeContainer,
						Layout: LayoutTabbed,
						Children: []Child{
							{Size: 100, Node: Node{ID: "primary", Kind: NodeSlot, Role: "primary"}},
						},
					},
				},
				{
					Size: 100 - primaryWidth,
					Node: Node{
						ID:     "secondary-tabs",
						Kind:   NodeContainer,
						Layout: LayoutTabbed,
						Children: []Child{
							{Size: 100, Node: Node{ID: "secondary", Kind: NodeSlot, Role: "secondary"}},
						},
					},
				},
			},
		},
		Policy: LayoutPolicy{
			DefaultSlot:    "primary",
			InsertionPoint: "end",
		},
	}
}

// NewSpiral returns an autotiling/spiral layout spec.
// This is a simple two-zone layout where the engine handles the spiral
// subdivision dynamically based on window count and dimensions.
//
//	┌───┬───────────────────┐
//	│   │         2         │
//	│   ├─────────┬─────────┤
//	│ 1 │    3    │    4    │
//	│   │         ├────┬────┤
//	│   │         │ 5  │ 6  │
//	└───┴─────────┴────┴────┘
func NewSpiral() *LayoutSpec {
	return &LayoutSpec{
		Name: "spiral",
		Root: Node{
			ID:   "all",
			Kind: NodeSlot,
			Role: "all",
		},
		Policy: LayoutPolicy{
			DefaultSlot:    "all",
			InsertionPoint: "end",
		},
	}
}

// BuiltinSpecs returns all built-in layout specs with default settings.
func BuiltinSpecs() map[string]*LayoutSpec {
	return map[string]*LayoutSpec{
		"master-stack": NewMasterStack(50),
		"grid":         NewGrid(),
		"columns":      NewColumns([]float64{33, 34, 33}),
		"dual-tabbed":  NewDualTabbed(50),
		"spiral":       NewSpiral(),
	}
}
