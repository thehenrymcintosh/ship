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
}

// GraphEdge is one edge of the pipeline graph.
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // outcome, error, exhausted, came_from
	Taken bool   `json:"taken"`
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
		g.Nodes = append(g.Nodes, GraphNode{ID: name, Type: t, Label: name, Description: desc})
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
	return g
}

// Mermaid renders the graph as a Mermaid flowchart.
func (p *Pipeline) Mermaid() string {
	g := p.Graph()
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	id := func(s string) string {
		return "n_" + strings.NewReplacer("-", "_", "$", "_", " ", "_").Replace(s)
	}
	esc := func(s string) string { return strings.ReplaceAll(s, `"`, "#quot;") }
	for _, n := range g.Nodes {
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
		arrow := "-->"
		if e.Kind != "outcome" {
			arrow = "-.->"
		}
		fmt.Fprintf(&b, "  %s %s|%s| %s\n", id(e.From), arrow, esc(e.Label), id(e.To))
	}
	return b.String()
}
