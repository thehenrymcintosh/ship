package pipeline

import (
	"fmt"
	"strings"
)

// GraphNode is one node of the pipeline graph.
type GraphNode struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // step type, or "done"/"stop"
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Visits      int    `json:"visits"`
	Current     bool   `json:"current"`
	// Recorded is the step's visits over every recorded run (a pipeline's
	// graph, where Visits stays 0).
	Recorded int `json:"recorded,omitempty"`
	// Outcomes counts the visits' outcomes (a run's, or every recorded
	// run's on a pipeline's graph).
	Outcomes map[string]int `json:"outcomes,omitempty"`
	// Fallback marks a catch-all check-in: an ask step that error or
	// exhausted routes lead to. Graphs hide it unless a run went there.
	Fallback bool `json:"fallback,omitempty"`
	// Detail is what the step does, for the step panel (steps only).
	Detail *StepDetail `json:"detail,omitempty"`
	// History lists the step's visits in a run (run graphs only).
	History []GraphVisit `json:"history,omitempty"`
}

// GraphVisit is one visit of a step in a run, for the step panel.
type GraphVisit struct {
	Seq        int     `json:"seq"`
	Number     int     `json:"number"`
	Outcome    string  `json:"outcome,omitempty"`
	Running    bool    `json:"running,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	Summary    string  `json:"summary,omitempty"`
}

// GraphEdge is one edge of the pipeline graph.
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // outcome, error, exhausted, came_from
	Taken bool   `json:"taken"`
	// Count is how many times it was taken.
	Count int `json:"count,omitempty"`
	// Fallback marks an implicit route: error/exhausted routing, or any
	// edge into or out of a fallback check-in.
	Fallback bool `json:"fallback,omitempty"`
}

// Graph is the pipeline as nodes and edges.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// TypeGlyph returns the UI glyph for a step type.
func TypeGlyph(t string) string {
	switch t {
	case TypeAgent:
		return "◆"
	case TypeRun:
		return "▢"
	case TypeAsk:
		return "●"
	case TypeWait:
		return "⏳"
	case TypeSplit:
		return "⑂"
	case TypeFanout:
		return "☰"
	case TypePR:
		return "⇅"
	case TargetDone:
		return "✓"
	case TargetStop:
		return "■"
	}
	return ""
}

// Graph builds the graph. `$came_from` edges are expanded back to every step
// that can route into the source, with kind came_from.
func (p *Pipeline) Graph() Graph {
	var g Graph
	used := map[string]bool{}
	for _, name := range p.SortedSteps() {
		s := p.Steps[name]
		t := ""
		if s != nil {
			t = s.Type()
		}
		desc := ""
		if s != nil {
			desc = s.Description
		}
		g.Nodes = append(g.Nodes, GraphNode{ID: name, Type: t, Label: name, Description: desc, Detail: p.StepDetail(name)})
	}
	static := p.Edges()
	preds := map[string][]string{}
	for _, e := range static {
		if e.To != TargetCameFrom {
			preds[e.To] = append(preds[e.To], e.From)
		}
	}
	seen := map[string]bool{}
	addEdge := func(e GraphEdge) {
		k := e.From + "\x00" + e.To + "\x00" + e.Label
		if seen[k] {
			return
		}
		seen[k] = true
		used[e.To] = true
		g.Edges = append(g.Edges, e)
	}
	for _, e := range static {
		if e.To == TargetCameFrom {
			for _, from := range uniq(preds[e.From]) {
				addEdge(GraphEdge{From: e.From, To: from, Label: e.Label, Kind: "came_from"})
			}
			continue
		}
		addEdge(GraphEdge{From: e.From, To: e.To, Label: e.Label, Kind: e.Kind})
	}
	for _, t := range []string{TargetDone, TargetStop} {
		if used[t] {
			g.Nodes = append(g.Nodes, GraphNode{ID: t, Type: t, Label: t})
		}
	}
	// Catch-all check-ins: ask steps that only error or exhausted routes
	// lead to. One a step's outcome also asks for stays in the flow.
	fallback, explicit := map[string]bool{}, map[string]bool{}
	for _, e := range static {
		s := p.Steps[e.To]
		if s == nil || s.Type() != TypeAsk || e.From == e.To {
			continue
		}
		if e.Kind == "error" || e.Kind == "exhausted" {
			fallback[e.To] = true
		} else {
			explicit[e.To] = true
		}
	}
	for id := range explicit {
		delete(fallback, id)
	}
	// done/stop count as fallback too when only fallback routes reach them.
	reached := map[string]bool{}
	for i := range g.Edges {
		e := &g.Edges[i]
		e.Fallback = e.Kind == "error" || e.Kind == "exhausted" || fallback[e.From] || fallback[e.To]
		if !e.Fallback {
			reached[e.To] = true
		}
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		n.Fallback = fallback[n.ID] || ((n.ID == TargetDone || n.ID == TargetStop) && !reached[n.ID])
	}
	return g
}

// Mermaid renders the graph as a Mermaid flowchart.
func (p *Pipeline) Mermaid() string {
	g := p.Graph()
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	id := func(s string) string {
		return "n_" + strings.NewReplacer("-", "_", "$", "_", " ", "_").Replace(s)
	}
	esc := func(s string) string { return strings.ReplaceAll(s, `"`, "#quot;") }
	hidden := 0
	for _, n := range g.Nodes {
		if n.Fallback {
			hidden++
			continue
		}
		label := esc(strings.TrimSpace(TypeGlyph(n.Type) + " " + n.Label))
		switch n.Type {
		case TypeAsk:
			fmt.Fprintf(&b, "  %s([\"%s\"])\n", id(n.ID), label)
		case TypeRun, TypeWait:
			fmt.Fprintf(&b, "  %s[\"%s\"]\n", id(n.ID), label)
		case TargetDone, TargetStop:
			fmt.Fprintf(&b, "  %s(((\"%s\")))\n", id(n.ID), label)
		case TypeSplit, TypeFanout:
			fmt.Fprintf(&b, "  %s[[\"%s\"]]\n", id(n.ID), label)
		default:
			fmt.Fprintf(&b, "  %s{{\"%s\"}}\n", id(n.ID), label)
		}
	}
	fmt.Fprintf(&b, "  start((start)) --> %s\n", id(p.Start))
	for _, e := range g.Edges {
		if e.Fallback {
			continue
		}
		arrow := "-->"
		if e.Kind != "outcome" {
			arrow = "-.->"
		}
		fmt.Fprintf(&b, "  %s %s|%s| %s\n", id(e.From), arrow, esc(e.Label), id(e.To))
	}
	if hidden > 0 {
		b.WriteString("  %% catch-all check-ins (reached on errors or when a step gives up) are left out\n")
	}
	return b.String()
}
