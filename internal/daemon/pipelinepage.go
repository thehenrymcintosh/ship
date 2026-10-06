package daemon

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/engine/steps"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/stats"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// PipelinePage is /pipelines/{name}: how a pipeline is doing, version by
// version.
type PipelinePage struct {
	Name, Repo, Query string
	Description       string
	Global            bool
	Where             string // where its history lives
	HasGraph          bool
	Findings          []pipeline.Finding
	Current           string // label of the version as the files are now
	Backfilled        int

	Scope   string // "done": time and cost from runs that finished done; "all": every finished run
	MinRuns int

	Overview Overview
	Trend    []VersionRow
	Steps    []StepRow
	Versions []VersionRow
	Compare  *CompareView
	Feedback []history.Item
	OpenFB   int
}

// Overview is the headline numbers over every recorded run.
type Overview struct {
	Runs, Done           int
	Success              float64
	SuccessLo, SuccessHi float64
	MedianMS             int64
	MedianCost           float64
	HandsOn              float64 // check-ins plus interventions per run
	CheckIns             float64
	Interventions        float64
	WaitMS               int64   // median time waiting for a person
	Autonomous           float64 // share of runs nobody touched before the PR
	PRRuns               int
	PRRounds             float64
	Feedback             int
	DurationHist         template.HTML
	CostHist             template.HTML
}

// VersionRow is one version with its runs.
type VersionRow struct {
	history.Version
	Label      string
	Runs       int
	Success    float64
	MedianMS   int64
	MedianCost float64
	HandsOn    float64
	Feedback   int
	Current    bool
}

// StepRow is one step across runs.
type StepRow struct {
	Step, Type   string
	Visits       float64 // per run
	PassRate     float64
	Decides      bool
	Passes       int
	Fails        int
	Total        int
	MedianMS     int64
	CostPerRun   float64
	TokensPerRun int64
	NeedsPerson  float64
	Outcomes     []KV
	Hist         template.HTML
	Advice       []stats.Advice
}

// KV is a label and count.
type KV struct {
	K string
	V int
}

// CompareView compares two versions.
type CompareView struct {
	A, B     string // labels
	AHash    string
	BHash    string
	Options  []VersionRow
	NA, NB   int
	Metrics  []MetricView
	Steps    []StepCompare
	Hists    []HistPair
	Headline string
}

// MetricView is a comparison ready to show.
type MetricView struct {
	stats.Comparison
	AText, BText string
	Change       string
	Explain      string
	Box          template.HTML
}

// StepCompare is one step's comparisons.
type StepCompare struct {
	Step    string
	Metrics []MetricView
}

// HistPair overlays two versions' distributions.
type HistPair struct {
	Title string
	SVG   template.HTML
}

func (d *Daemon) pagePipeline(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	q := r.URL.Query()
	repo := q.Get("repo")
	l := d.eng.Loader(repo)
	f, findings := l.Load(name)
	if f == nil {
		msg := "pipeline not found"
		if len(findings) > 0 {
			msg = findings[0].String()
		}
		http.Error(w, msg, 404)
		return
	}
	cfg, _ := d.eng.Config(repo)
	v := &PipelinePage{Name: name, Repo: repo, Description: f.Pipeline.Description, HasGraph: true, Scope: "done", MinRuns: cfg.Stats.MinRuns}
	if v.MinRuns < 2 {
		v.MinRuns = stats.MinRuns
	}
	if q.Get("scope") == "all" {
		v.Scope = "all"
	}
	vals := url.Values{}
	if repo != "" {
		vals.Set("repo", repo)
	}
	v.Query = vals.Encode()
	v.Global = repo == "" || l.Dir(name) != engine.PipelinesDir(repo)
	opts := d.eng.ValidateOptions(cfg)
	opts.SkillExists = d.eng.SkillCheck(repo)
	_, v.Findings = l.Validate(name, opts)
	dir := engine.HistoryDir(l, repo, d.eng.Home(), name)
	v.Where = tildePath(dir)
	hs := d.eng.HistoryStore(dir)
	label, _ := d.eng.PipelineStatus(repo, name)
	v.Current = label

	runs, _ := hs.Runs()
	if len(runs) == 0 {
		if n, err := d.eng.BackfillStats(repo, name); err == nil && n > 0 {
			v.Backfilled = n
			runs, _ = hs.Runs()
		}
	}
	versions, _ := hs.Versions()
	items, _ := hs.Items()
	v.Feedback = items
	for _, it := range items {
		if it.Status == "open" {
			v.OpenFB++
		}
	}

	v.Overview = overview(runs, v.Scope)
	byVersion := map[string][]history.RunStats{}
	for _, rs := range runs {
		byVersion[rs.Version] = append(byVersion[rs.Version], rs)
	}
	fbByVersion := map[int]int{}
	for _, it := range items {
		fbByVersion[it.Version]++
	}
	for _, ver := range versions {
		row := versionRow(ver, byVersion[ver.Hash], v.Scope)
		row.Feedback = fbByVersion[ver.Version]
		row.Current = strings.HasPrefix(label, ver.Label())
		v.Versions = append(v.Versions, row)
		if row.Runs > 0 {
			v.Trend = append(v.Trend, row)
		}
	}
	// Newest first in the list.
	for i, j := 0, len(v.Versions)-1; i < j; i, j = i+1, j-1 {
		v.Versions[i], v.Versions[j] = v.Versions[j], v.Versions[i]
	}
	v.Steps = stepRows(f.Pipeline, runs, v.Scope)
	v.Compare = compareVersions(f.Pipeline, v.Trend, byVersion, q.Get("a"), q.Get("b"), v.Scope, v.MinRuns)
	d.render(w, "pipeline", "layout", d.layout(name, "pipeline", v))
}

// tildePath shows a path under the home dir as ~/….
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// scoped are the runs whose time and cost count.
func scoped(runs []history.RunStats, scope string) []history.RunStats {
	if scope == "all" {
		return runs
	}
	var out []history.RunStats
	for _, r := range runs {
		if r.Status == store.StatusDone {
			out = append(out, r)
		}
	}
	return out
}

func field(runs []history.RunStats, get func(history.RunStats) float64) []float64 {
	out := make([]float64, 0, len(runs))
	for _, r := range runs {
		out = append(out, get(r))
	}
	return out
}

var (
	runMS     = func(r history.RunStats) float64 { return float64(r.DurationMS) }
	runCost   = func(r history.RunStats) float64 { return r.CostUSD }
	runTokens = func(r history.RunStats) float64 { return float64(r.Tokens) }
	runHands  = func(r history.RunStats) float64 { return float64(r.Human.HandsOn()) }
	runWait   = func(r history.RunStats) float64 { return float64(r.Human.WaitMS) }
)

func countDone(runs []history.RunStats) int {
	n := 0
	for _, r := range runs {
		if r.Status == store.StatusDone {
			n++
		}
	}
	return n
}

func mean(xs []float64) float64 { return stats.Describe(xs).Mean }

func overview(runs []history.RunStats, scope string) Overview {
	o := Overview{Runs: len(runs), Done: countDone(runs)}
	if o.Runs == 0 {
		return o
	}
	o.Success = float64(o.Done) / float64(o.Runs)
	o.SuccessLo, o.SuccessHi = stats.RateInterval(o.Done, o.Runs)
	sc := scoped(runs, scope)
	ms, cost := field(sc, runMS), field(sc, runCost)
	o.MedianMS, o.MedianCost = int64(stats.Describe(ms).Median), stats.Describe(cost).Median
	o.HandsOn = mean(field(runs, runHands))
	o.CheckIns = mean(field(runs, func(r history.RunStats) float64 { return float64(r.Human.CheckIns) }))
	o.Interventions = mean(field(runs, func(r history.RunStats) float64 { return float64(r.Human.Corrective()) }))
	o.WaitMS = int64(stats.Describe(field(runs, runWait)).Median)
	auto := 0
	for _, r := range runs {
		if r.Human.Autonomous {
			auto++
		}
		if r.PR != nil {
			o.PRRuns++
			o.PRRounds += float64(r.PR.Rounds)
		}
		o.Feedback += r.Feedback
	}
	o.Autonomous = float64(auto) / float64(o.Runs)
	if o.PRRuns > 0 {
		o.PRRounds /= float64(o.PRRuns)
	}
	o.DurationHist = histSVG(ms, nil, fmtMS)
	o.CostHist = histSVG(cost, nil, fmtUSD)
	return o
}

func versionRow(v history.Version, runs []history.RunStats, scope string) VersionRow {
	row := VersionRow{Version: v, Label: fmt.Sprintf("v%d", v.Version), Runs: len(runs)}
	if len(runs) == 0 {
		return row
	}
	row.Success = float64(countDone(runs)) / float64(len(runs))
	sc := scoped(runs, scope)
	row.MedianMS = int64(stats.Describe(field(sc, runMS)).Median)
	row.MedianCost = stats.Describe(field(sc, runCost)).Median
	row.HandsOn = mean(field(runs, runHands))
	return row
}

// outcomeClass says whether an outcome moved the run on ("pass") or sent
// it back, stopped it or errored ("fail"), judged by the pipeline's graph:
// an outcome passes when it leads to a step nearer to done (fewer steps
// away) than this one. "" when it can't tell (ask choices, outcomes the
// pipeline no longer has).
func outcomeClass(p *pipeline.Pipeline, dist map[string]int, step, outcome string) string {
	if outcome == "error" {
		return "fail"
	}
	s := p.Steps[step]
	if s == nil || s.Type() == pipeline.TypeAsk {
		return ""
	}
	t, ok := s.Target(outcome)
	switch {
	case !ok:
		return ""
	case t == pipeline.TargetStop || t == pipeline.TargetCameFrom || t == step:
		return "fail"
	case t == pipeline.TargetDone:
		return "pass"
	}
	dt, okT := dist[t]
	ds, okS := dist[step]
	switch {
	case okT && okS:
		if dt < ds {
			return "pass"
		}
		return "fail"
	case okS:
		return "fail" // somewhere with no way on to done (a check-in, say)
	case pipeline.Reachable(t, p.Edges(), nil)[step]:
		return "fail" // no way to done: back to this step is a failure
	}
	return "pass"
}

// distToDone is each step's fewest steps to done, over outcome edges.
func distToDone(p *pipeline.Pipeline) map[string]int {
	back := map[string][]string{}
	for _, e := range p.Edges() {
		if e.Kind == "outcome" {
			back[e.To] = append(back[e.To], e.From)
		}
	}
	dist := map[string]int{pipeline.TargetDone: 0}
	queue := []string{pipeline.TargetDone}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, from := range back[n] {
			if _, seen := dist[from]; !seen {
				dist[from] = dist[n] + 1
				queue = append(queue, from)
			}
		}
	}
	return dist
}

func decides(p *pipeline.Pipeline, step string) bool {
	s := p.Steps[step]
	return s != nil && s.Type() != pipeline.TypeAsk && len(s.OutcomeNames()) > 1
}

// stepAgg gathers one step's numbers over runs.
type stepAgg struct {
	typ                       string
	visits, passes, fails     int
	durations                 []float64
	cost                      float64
	tokens                    int64
	needs                     int
	outcomes                  map[string]int
	durShareSum, costShareSum float64
	scopedRuns                int
}

func aggregateSteps(p *pipeline.Pipeline, runs []history.RunStats, scope string) ([]string, map[string]*stepAgg) {
	var order []string
	aggs := map[string]*stepAgg{}
	dist := distToDone(p)
	inScope := map[string]bool{}
	for _, r := range scoped(runs, scope) {
		inScope[r.Run] = true
	}
	for _, r := range runs {
		var runDur int64
		for _, st := range r.Steps {
			for _, d := range st.DurationsMS {
				runDur += d
			}
		}
		for _, st := range r.Steps {
			a := aggs[st.Step]
			if a == nil {
				a = &stepAgg{typ: st.Type, outcomes: map[string]int{}}
				aggs[st.Step] = a
				order = append(order, st.Step)
			}
			a.visits += st.Visits
			for o, n := range st.Outcomes {
				a.outcomes[o] += n
				switch outcomeClass(p, dist, st.Step, o) {
				case "pass":
					a.passes += n
				case "fail":
					a.fails += n
				}
			}
			if st.Human.Needed() {
				a.needs++
			}
			if !inScope[r.Run] {
				continue
			}
			var d int64
			for _, x := range st.DurationsMS {
				a.durations = append(a.durations, float64(x))
				d += x
			}
			a.cost += st.CostUSD
			a.tokens += st.Tokens
			if runDur > 0 {
				a.durShareSum += float64(d) / float64(runDur)
			}
			if r.CostUSD > 0 {
				a.costShareSum += st.CostUSD / r.CostUSD
			}
		}
	}
	n := len(scoped(runs, scope))
	for _, a := range aggs {
		a.scopedRuns = n
	}
	return order, aggs
}

func stepRows(p *pipeline.Pipeline, runs []history.RunStats, scope string) []StepRow {
	order, aggs := aggregateSteps(p, runs, scope)
	var out []StepRow
	for _, name := range order {
		a := aggs[name]
		row := StepRow{Step: name, Type: a.typ, Decides: decides(p, name), Passes: a.passes, Fails: a.fails, Total: a.passes + a.fails}
		if len(runs) > 0 {
			row.Visits = float64(a.visits) / float64(len(runs))
			row.NeedsPerson = float64(a.needs) / float64(len(runs))
		}
		if row.Total > 0 {
			row.PassRate = float64(a.passes) / float64(row.Total)
		}
		if a.scopedRuns > 0 {
			row.CostPerRun = a.cost / float64(a.scopedRuns)
			row.TokensPerRun = a.tokens / int64(a.scopedRuns)
		}
		row.MedianMS = int64(stats.Describe(a.durations).Median)
		for o, n := range a.outcomes {
			row.Outcomes = append(row.Outcomes, KV{o, n})
		}
		sort.Slice(row.Outcomes, func(i, j int) bool { return row.Outcomes[i].V > row.Outcomes[j].V })
		row.Hist = sparkSVG(a.durations)
		facts := stats.StepFacts{Step: name, Decides: row.Decides, Visits: a.visits, Passes: a.passes, Fails: a.fails, Runs: a.scopedRuns, NeedsPerson: row.NeedsPerson}
		if s := p.Steps[name]; s != nil {
			facts.Agent = s.IsAgentLike()
		}
		if a.scopedRuns > 0 {
			facts.DurationShare = a.durShareSum / float64(a.scopedRuns)
			facts.CostShare = a.costShareSum / float64(a.scopedRuns)
		}
		row.Advice = stats.Advise(facts)
		out = append(out, row)
	}
	return out
}

func compareVersions(p *pipeline.Pipeline, withRuns []VersionRow, byVersion map[string][]history.RunStats, aRef, bRef, scope string, minN int) *CompareView {
	if len(withRuns) < 2 {
		return nil
	}
	vs := make([]history.Version, len(withRuns))
	for i, r := range withRuns {
		vs[i] = r.Version
	}
	a, b := &vs[len(vs)-2], &vs[len(vs)-1]
	if aRef != "" {
		if v, err := history.FindIn(vs, aRef); err == nil {
			a = v
		}
	}
	if bRef != "" {
		if v, err := history.FindIn(vs, bRef); err == nil {
			b = v
		}
	}
	cv := &CompareView{A: fmt.Sprintf("v%d", a.Version), B: fmt.Sprintf("v%d", b.Version), AHash: a.Hash, BHash: b.Hash, Options: withRuns}
	ra, rb := byVersion[a.Hash], byVersion[b.Hash]
	cv.NA, cv.NB = len(ra), len(rb)
	sa, sb := scoped(ra, scope), scoped(rb, scope)
	add := func(c stats.Comparison, fmtv func(float64) string, box bool, xa, xb []float64) MetricView {
		m := MetricView{Comparison: c, AText: fmtv(c.A), BText: fmtv(c.B), Explain: c.Explain(cv.A, cv.B)}
		switch c.Kind {
		case stats.KindRate:
			m.Change = fmt.Sprintf("%+.0f pts", c.Change*100)
		default:
			m.Change = fmt.Sprintf("×%.2f", c.Change)
		}
		if box {
			m.Box = boxSVG(xa, xb, fmtv)
		}
		return m
	}
	rate := func(metric string, runs []history.RunStats, ok func(history.RunStats) bool) (int, int) {
		n := 0
		for _, r := range runs {
			if ok(r) {
				n++
			}
		}
		return n, len(runs)
	}
	done := func(r history.RunStats) bool { return r.Status == store.StatusDone }
	auto := func(r history.RunStats) bool { return r.Human.Autonomous }
	sA, nA := rate("", ra, done)
	sB, nB := rate("", rb, done)
	cv.Metrics = append(cv.Metrics, add(stats.CompareRates("Success rate", sA, nA, sB, nB, false, minN), pctf, false, nil, nil))
	for _, m := range []struct {
		name string
		get  func(history.RunStats) float64
		f    func(float64) string
	}{{"Run time", runMS, fmtMS}, {"Run cost", runCost, fmtUSD}, {"Tokens per run", runTokens, fmtTok}} {
		xa, xb := field(sa, m.get), field(sb, m.get)
		cv.Metrics = append(cv.Metrics, add(stats.CompareContinuous(m.name, xa, xb, true, minN), m.f, true, xa, xb))
	}
	ha, hb := field(ra, runHands), field(rb, runHands)
	cv.Metrics = append(cv.Metrics, add(stats.CompareCounts("Hands-on per run", ha, hb, true, minN), num1, true, ha, hb))
	wa, wb := field(ra, runWait), field(rb, runWait)
	cv.Metrics = append(cv.Metrics, add(stats.CompareContinuous("Waiting for a person", wa, wb, true, minN), fmtMS, true, wa, wb))
	sA, _ = rate("", ra, auto)
	sB, _ = rate("", rb, auto)
	cv.Metrics = append(cv.Metrics, add(stats.CompareRates("Ran without a person", sA, nA, sB, nB, false, minN), pctf, false, nil, nil))
	var pa, pb []float64
	for _, r := range ra {
		if r.PR != nil {
			pa = append(pa, float64(r.PR.Rounds))
		}
	}
	for _, r := range rb {
		if r.PR != nil {
			pb = append(pb, float64(r.PR.Rounds))
		}
	}
	if len(pa)+len(pb) > 0 {
		cv.Metrics = append(cv.Metrics, add(stats.CompareCounts("PR review rounds", pa, pb, true, minN), num1, true, pa, pb))
	}

	cv.Hists = []HistPair{
		{"Run time", histSVG(field(sa, runMS), field(sb, runMS), fmtMS)},
		{"Run cost", histSVG(field(sa, runCost), field(sb, runCost), fmtUSD)},
	}

	// Per step.
	orderA, aggA := aggregateSteps(p, ra, scope)
	orderB, aggB := aggregateSteps(p, rb, scope)
	seen := map[string]bool{}
	for _, name := range append(orderB, orderA...) {
		if seen[name] {
			continue
		}
		seen[name] = true
		x, y := aggA[name], aggB[name]
		if x == nil {
			x = &stepAgg{}
		}
		if y == nil {
			y = &stepAgg{}
		}
		sc := StepCompare{Step: name}
		if decides(p, name) {
			sc.Metrics = append(sc.Metrics, add(stats.CompareRates("Pass rate", x.passes, x.passes+x.fails, y.passes, y.passes+y.fails, false, minN), pctf, false, nil, nil))
		}
		sc.Metrics = append(sc.Metrics, add(stats.CompareContinuous("Time per visit", x.durations, y.durations, true, minN), fmtMS, true, x.durations, y.durations))
		sc.Metrics = append(sc.Metrics, add(stats.CompareRates("Needs a person", x.needs, len(ra), y.needs, len(rb), true, minN), pctf, false, nil, nil))
		cv.Steps = append(cv.Steps, sc)
	}

	good, bad := 0, 0
	for _, m := range cv.Metrics {
		if m.Verdict.Good() {
			good++
		} else if m.Verdict.Bad() {
			bad++
		}
	}
	switch {
	case cv.NA < minN || cv.NB < minN:
		cv.Headline = fmt.Sprintf("Too few runs to compare yet: %d on %s and %d on %s (%d each needed).", cv.NA, cv.A, cv.NB, cv.B, minN)
	case good > 0 && bad == 0:
		cv.Headline = fmt.Sprintf("%s looks like an improvement on %s: better on %d of %d measures, worse on none.", cv.B, cv.A, good, len(cv.Metrics))
	case bad > 0 && good == 0:
		cv.Headline = fmt.Sprintf("%s looks worse than %s on %d of %d measures, and better on none.", cv.B, cv.A, bad, len(cv.Metrics))
	case good > 0:
		cv.Headline = fmt.Sprintf("Mixed: %s is better on %d measures and worse on %d.", cv.B, good, bad)
	default:
		cv.Headline = fmt.Sprintf("No clear difference between %s and %s yet.", cv.A, cv.B)
	}
	return cv
}

// --- formatting ----------------------------------------------------------------

func pctf(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }
func num1(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0") }
func fmtMS(f float64) string {
	if f <= 0 {
		return "0s"
	}
	return durMS(int64(f))
}
func fmtUSD(f float64) string {
	if f <= 0 {
		return "$0"
	}
	if s := money(f); s != "" {
		return s
	}
	return fmt.Sprintf("$%.3f", f)
}
func fmtTok(f float64) string { return steps.FormatTokens(int64(f)) }

// --- SVG -----------------------------------------------------------------------

// histSVG draws one sample's histogram, or two overlaid (a is the older
// version, b the newer), sharing bins.
func histSVG(a, b []float64, label func(float64) string) template.HTML {
	if len(a)+len(b) == 0 {
		return ""
	}
	lo, hi := stats.Range(a, b)
	log := stats.WantsLog(lo, hi)
	const n, w, h = 16, 320, 90
	ha, hb := stats.Histogram(a, lo, hi, n, log), stats.Histogram(b, lo, hi, n, log)
	maxC := 1
	for i := range ha {
		maxC = max(maxC, ha[i].Count, hb[i].Count)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="hist" viewBox="0 0 %d %d" role="img" aria-label="histogram from %s to %s">`, w, h+16, template.HTMLEscapeString(label(lo)), template.HTMLEscapeString(label(hi)))
	bw := float64(w) / n
	bar := func(bins []stats.Bin, class string, inset float64) {
		for i, bn := range bins {
			if bn.Count == 0 {
				continue
			}
			bh := float64(bn.Count) / float64(maxC) * h
			fmt.Fprintf(&sb, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f"><title>%s to %s: %d</title></rect>`,
				class, float64(i)*bw+inset, h-bh, bw-1-2*inset, bh, template.HTMLEscapeString(label(bn.Lo)), template.HTMLEscapeString(label(bn.Hi)), bn.Count)
		}
	}
	if b == nil {
		bar(ha, "bar-b", 0)
	} else {
		bar(ha, "bar-a", 0)
		bar(hb, "bar-b", bw/5)
	}
	fmt.Fprintf(&sb, `<line class="axis" x1="0" y1="%d" x2="%d" y2="%d"/>`, h, w, h)
	fmt.Fprintf(&sb, `<text class="tick" x="0" y="%d">%s</text><text class="tick" x="%d" y="%d" text-anchor="end">%s</text>`, h+13, template.HTMLEscapeString(label(lo)), w, h+13, template.HTMLEscapeString(label(hi)))
	if log {
		fmt.Fprintf(&sb, `<text class="tick" x="%d" y="%d" text-anchor="middle">log scale</text>`, w/2, h+13)
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// sparkSVG is a tiny histogram for a table cell.
func sparkSVG(xs []float64) template.HTML {
	if len(xs) < 2 {
		return ""
	}
	lo, hi := stats.Range(xs)
	bins := stats.Histogram(xs, lo, hi, 10, stats.WantsLog(lo, hi))
	maxC := 1
	for _, b := range bins {
		maxC = max(maxC, b.Count)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="spark" viewBox="0 0 60 16" role="img" aria-label="time per visit, %s to %s">`, fmtMS(lo), fmtMS(hi))
	for i, b := range bins {
		if b.Count == 0 {
			continue
		}
		bh := math.Max(1, float64(b.Count)/float64(maxC)*16)
		fmt.Fprintf(&sb, `<rect class="bar-b" x="%d" y="%.1f" width="5" height="%.1f"/>`, i*6, 16-bh, bh)
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// boxSVG draws two box plots (older above, newer below) on a shared scale:
// whiskers p10 to p90, box p25 to p75, a tick at the median.
func boxSVG(a, b []float64, label func(float64) string) template.HTML {
	if len(a) == 0 && len(b) == 0 {
		return ""
	}
	lo, hi := stats.Range(a, b)
	if hi <= lo {
		hi = lo + 1
	}
	const w, rowH = 240, 18
	x := func(v float64) float64 { return 4 + (v-lo)/(hi-lo)*(w-8) }
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="box" viewBox="0 0 %d %d" role="img" aria-label="distributions from %s to %s">`, w, rowH*2+14, template.HTMLEscapeString(label(lo)), template.HTMLEscapeString(label(hi)))
	row := func(xs []float64, y int, class string) {
		if len(xs) == 0 {
			return
		}
		s := stats.Describe(xs)
		mid := float64(y) + rowH/2
		fmt.Fprintf(&sb, `<g class="%s"><title>median %s, middle half %s to %s, n=%d</title>`, class, template.HTMLEscapeString(label(s.Median)), template.HTMLEscapeString(label(s.P25)), template.HTMLEscapeString(label(s.P75)), s.N)
		fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, x(s.P10), mid, x(s.P90), mid)
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%d" width="%.1f" height="%d"/>`, x(s.P25), y+3, math.Max(1, x(s.P75)-x(s.P25)), rowH-6)
		fmt.Fprintf(&sb, `<line class="median" x1="%.1f" y1="%d" x2="%.1f" y2="%d"/></g>`, x(s.Median), y+2, x(s.Median), y+rowH-2)
	}
	row(a, 0, "box-a")
	row(b, rowH, "box-b")
	fmt.Fprintf(&sb, `<text class="tick" x="0" y="%d">%s</text><text class="tick" x="%d" y="%d" text-anchor="end">%s</text></svg>`, rowH*2+12, template.HTMLEscapeString(label(lo)), w, rowH*2+12, template.HTMLEscapeString(label(hi)))
	return template.HTML(sb.String())
}
