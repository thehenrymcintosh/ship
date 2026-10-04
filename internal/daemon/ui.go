package daemon

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/merlin-digital/ship/internal/brand"
	"github.com/merlin-digital/ship/internal/brief"
	"github.com/merlin-digital/ship/internal/engine"
	"github.com/merlin-digital/ship/internal/pipeline"
	"github.com/merlin-digital/ship/internal/store"
	"github.com/merlin-digital/ship/web"
)

// --- templates -------------------------------------------------------------

var funcs = template.FuncMap{
	"md":     Markdown,
	"glyph":  pipeline.TypeGlyph,
	"ago":    ago,
	"dur":    durMS,
	"money":  money,
	"short":  shortID,
	"trunc":  truncRunes,
	"pretty": prettyJSON,
	"join":   strings.Join,
	"brand":  func() string { return brand.Name },
	"phase":  phase,
	"plural": func(n int, s string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, s)
		}
		return fmt.Sprintf("%d %ss", n, s)
	},
	"inc": func(i int) int { return i + 1 },
	"deref": func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	},
	"terminal": func(s store.Status) bool { return s.Terminal() },
	"inInbox":  func(s store.Status) bool { return s.InInbox() },
	"basename": filepath.Base,
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

type pages struct {
	once sync.Once
	m    map[string]*template.Template
	err  error
}

var tpl pages

func (p *pages) get(name string) (*template.Template, error) {
	p.once.Do(func() {
		base, err := template.New("").Funcs(funcs).ParseFS(web.Templates, "templates/layout.html", "templates/partials.html")
		if err != nil {
			p.err = err
			return
		}
		p.m = map[string]*template.Template{"": base}
		for _, page := range []string{"runs", "run", "inbox", "pipelines"} {
			t, err := template.Must(base.Clone()).ParseFS(web.Templates, "templates/"+page+".html")
			if err != nil {
				p.err = err
				return
			}
			p.m[page] = t
		}
	})
	if p.err != nil {
		return nil, p.err
	}
	t, ok := p.m[name]
	if !ok {
		return nil, fmt.Errorf("no template %q", name)
	}
	return t, nil
}

func (d *Daemon) render(w http.ResponseWriter, page, name string, data any) {
	t, err := tpl.get(page)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}

// --- helpers for templates ---------------------------------------------------

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func durMS(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return fmtDur(time.Duration(ms) * time.Millisecond)
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func money(f float64) string {
	if f <= 0 {
		return ""
	}
	if f < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", math.Round(f*100)/100)
}

func shortID(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 && !strings.Contains(id[i:], ".") {
		return id[i+1:]
	}
	if i := strings.LastIndex(id, "."); i > 0 {
		return id[i+1:]
	}
	return id
}

func truncRunes(n int, s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func prettyJSON(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}

// phase is the kitchen word for where a run is: "cooking", "at the pass"…
func phase(s *store.RunSnapshot, p *pipeline.Pipeline) string {
	switch s.Status {
	case store.StatusStarting:
		return "prepping"
	case store.StatusRunning:
		if lv := s.LastVisit(); lv != nil && lv.Queued {
			return "queued"
		}
		return "cooking"
	case store.StatusWaiting:
		return "at the pass"
	case store.StatusAsking, store.StatusNeedsAttention:
		return "needs you"
	case store.StatusFannedOut:
		return "on the line"
	case store.StatusDone:
		return "served"
	}
	return string(s.Status)
}

// --- view models -------------------------------------------------------------

// RunRow is one row of the runs list.
type RunRow struct {
	S        *store.RunSnapshot
	Children []*store.RunSnapshot
	Kitchen  string // "3 slices on the line · 1 at the pass · …"
	Elapsed  string
	Inbox    bool
}

// RepoGroup groups rows by repo.
type RepoGroup struct {
	Repo string
	Name string
	Rows []RunRow
}

func elapsed(s *store.RunSnapshot) string {
	end := time.Now()
	if s.FinishedAt != nil {
		end = *s.FinishedAt
	}
	return fmtDur(end.Sub(s.CreatedAt).Round(time.Second))
}

func kitchenLine(slices int, kids []*store.RunSnapshot) string {
	counts := map[string]int{}
	order := []string{"needs you", "cooking", "at the pass", "queued", "served", "stopped", "failed", "cancelled", "prepping"}
	for _, k := range kids {
		counts[phase(k, nil)]++
	}
	if waiting := slices - len(kids); waiting > 0 {
		counts["queued"] += waiting
	}
	parts := []string{fmt.Sprintf("%d slices on the line", slices)}
	for _, o := range order {
		if counts[o] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[o], o))
		}
	}
	return strings.Join(parts, " · ")
}

func (d *Daemon) runGroups(filter string) ([]RepoGroup, error) {
	all, err := d.runList("", "", "", 0)
	if err != nil {
		return nil, err
	}
	byParent := map[string][]*store.RunSnapshot{}
	for _, s := range all {
		if s.Parent != nil {
			byParent[s.Parent.ID] = append(byParent[s.Parent.ID], s)
		}
	}
	groups := map[string]*RepoGroup{}
	var order []string
	for _, s := range all {
		if s.Parent != nil {
			continue
		}
		kids := byParent[s.ID]
		sort.Slice(kids, func(i, j int) bool { return kids[i].ID < kids[j].ID })
		match := statusMatches(filter, s)
		for _, k := range kids {
			if filter == "inbox" && statusMatches("inbox", k) {
				match = true
			}
		}
		if !match {
			continue
		}
		row := RunRow{S: s, Children: kids, Elapsed: elapsed(s), Inbox: s.Status.InInbox()}
		if len(s.Slices) > 0 && len(kids) > 0 {
			row.Kitchen = kitchenLine(len(s.Slices), kids)
		}
		g := groups[s.Repo]
		if g == nil {
			g = &RepoGroup{Repo: s.Repo, Name: filepath.Base(s.Repo)}
			groups[s.Repo] = g
			order = append(order, s.Repo)
		}
		g.Rows = append(g.Rows, row)
	}
	out := make([]RepoGroup, 0, len(order))
	for _, r := range order {
		out = append(out, *groups[r])
	}
	return out, nil
}

// Panel is a titled block shown with a question.
type Panel struct {
	Title string
	HTML  template.HTML
}

// SliceView is a slice card in the split review.
type SliceView struct {
	Number     int
	Key        string
	Title      string
	Acceptance []string
	Body       template.HTML
}

// RunView is the run page.
type RunView struct {
	S          *store.RunSnapshot
	P          *pipeline.Pipeline
	BriefHTML  template.HTML
	Acceptance []string
	Children   []*store.RunSnapshot
	Visits     []store.VisitSummary // newest first
	Steps      []string
	Panels     []Panel
	Slices     []SliceView
	VarNames   []string
	VarDesc    map[string]string
	StepTypes  map[string]string
	CanResume  bool
	CanRetry   bool
	Executing  bool
	Kitchen    string
	ParentT    string
	Bypass     bool
}

func (d *Daemon) runView(id string) (*RunView, error) {
	s, err := d.eng.Snapshot(id)
	if err != nil {
		return nil, err
	}
	v := &RunView{S: s, VarDesc: map[string]string{}, StepTypes: map[string]string{}}
	v.P, _ = d.eng.RunPipeline(id)
	dir := d.st.RunDir(id)
	if b, err := os.ReadFile(filepath.Join(dir, store.BriefFile)); err == nil {
		if br, err := brief.Parse(b); err == nil {
			v.BriefHTML = Markdown(br.Body)
			v.Acceptance = br.Acceptance
		}
	}
	for i := len(s.Visits) - 1; i >= 0; i-- {
		v.Visits = append(v.Visits, s.Visits[i])
	}
	if v.P != nil {
		v.Steps = v.P.SortedSteps()
		for _, n := range v.Steps {
			v.StepTypes[n] = v.P.Steps[n].Type()
			if v.P.Steps[n].PermissionMode == "bypassPermissions" {
				v.Bypass = true
			}
		}
		if v.P.Agent != nil && v.P.Agent.PermissionMode == "bypassPermissions" {
			v.Bypass = true
		}
		v.VarNames = v.P.SortedVars()
		for _, n := range v.VarNames {
			if vr := v.P.Variables[n]; vr != nil {
				v.VarDesc[n] = vr.Description
			}
		}
	}
	kids, _ := d.runList("", "", id, 0)
	sort.Slice(kids, func(i, j int) bool { return kids[i].ID < kids[j].ID })
	v.Children = kids
	if len(s.Slices) > 0 && len(kids) > 0 {
		v.Kitchen = kitchenLine(len(s.Slices), kids)
	}
	if s.Parent != nil {
		if ps, err := d.eng.Snapshot(s.Parent.ID); err == nil {
			v.ParentT = ps.Title
		}
	}
	lv := s.LastVisit()
	v.CanResume = s.Status == store.StatusNeedsAttention && lv != nil && lv.Interrupted && lv.SessionID != ""
	v.CanRetry = s.Status == store.StatusNeedsAttention || (s.Status == store.StatusAsking && s.PendingAsk != nil && s.PendingAsk.Kind == store.AskKindAsk && s.CameFrom != "")
	v.Executing = lv != nil && lv.Running()

	if a := s.PendingAsk; a != nil {
		for _, show := range a.Show {
			switch show {
			case "prev.handover":
				if pv := prevOf(s, a.Seq); pv != nil {
					if b, err := os.ReadFile(filepath.Join(dir, store.VisitsDir, pv.Dir, "handover.md")); err == nil {
						v.Panels = append(v.Panels, Panel{Title: "Handover from " + pv.Step, HTML: Markdown(string(b))})
					}
				}
			case "brief":
				v.Panels = append(v.Panels, Panel{Title: "Brief", HTML: v.BriefHTML})
			case "vars":
				var sb strings.Builder
				for _, n := range sortedVarKeys(s.Vars) {
					fmt.Fprintf(&sb, "- **%s** = `%s`\n", n, s.Vars[n])
				}
				v.Panels = append(v.Panels, Panel{Title: "Variables", HTML: Markdown(sb.String())})
			}
		}
		if a.Kind == store.AskKindSplitReview {
			for _, sl := range s.ProposedSlices {
				sv := SliceView{Number: sl.Number, Key: sl.Key, Title: sl.Title}
				if b, err := os.ReadFile(sl.File); err == nil {
					if br, err := brief.Parse(b); err == nil {
						sv.Acceptance = br.Acceptance
						sv.Body = Markdown(br.Body)
					}
				}
				v.Slices = append(v.Slices, sv)
			}
		}
	}
	return v, nil
}

func prevOf(s *store.RunSnapshot, seq int) *store.VisitSummary {
	for i := len(s.Visits) - 1; i >= 0; i-- {
		if s.Visits[i].Seq < seq && s.Visits[i].Finished != nil {
			return &s.Visits[i]
		}
	}
	return s.LastFinishedVisit()
}

func sortedVarKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- visit tabs --------------------------------------------------------------

// FeedItem is one entry of an agent's live feed.
type FeedItem struct {
	Kind    string // text | tool_use | tool_result | system
	Text    template.HTML
	Name    string
	Detail  string
	IsError bool
}

func readTail(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() > max {
		f.Seek(st.Size()-max, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	if st.Size() > max {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
		return "… (earlier output trimmed)\n" + string(b)
	}
	return string(b)
}

// toolSummary renders a one-line description of a tool call input.
func toolSummary(name string, input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description", "prompt", "outcome"} {
		if v, ok := m[k].(string); ok && v != "" {
			return truncRunes(140, v)
		}
	}
	return ""
}

// transcriptFeed parses a stream-json transcript into feed items.
func transcriptFeed(path string) []FeedItem {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []FeedItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var l struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Message *struct {
				Content []struct {
					Type    string          `json:"type"`
					Text    string          `json:"text"`
					Name    string          `json:"name"`
					Input   json.RawMessage `json:"input"`
					Content json.RawMessage `json:"content"`
					IsError bool            `json:"is_error"`
				} `json:"content"`
			} `json:"message"`
			Result  json.RawMessage `json:"result"`
			IsError bool            `json:"is_error"`
			Cost    float64         `json:"total_cost_usd"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch l.Type {
		case "assistant", "user":
			if l.Message == nil {
				continue
			}
			for _, c := range l.Message.Content {
				switch c.Type {
				case "text":
					if strings.TrimSpace(c.Text) != "" {
						out = append(out, FeedItem{Kind: "text", Text: Markdown(c.Text)})
					}
				case "tool_use":
					out = append(out, FeedItem{Kind: "tool_use", Name: c.Name, Text: template.HTML(template.HTMLEscapeString(toolSummary(c.Name, c.Input))), Detail: prettyJSON(c.Input)})
				case "tool_result":
					var s string
					if json.Unmarshal(c.Content, &s) != nil {
						s = string(c.Content)
					}
					out = append(out, FeedItem{Kind: "tool_result", Detail: truncRunes(4000, s), IsError: c.IsError})
				}
			}
		case "result":
			msg := fmt.Sprintf("Finished (%s)", l.Subtype)
			if l.Cost > 0 {
				msg += " · " + money(l.Cost)
			}
			out = append(out, FeedItem{Kind: "system", Text: template.HTML(template.HTMLEscapeString(msg)), IsError: l.IsError})
		}
	}
	return out
}

// TabView is a visit tab's content.
type TabView struct {
	RunID  string
	Seq    int
	Tab    string
	Type   string
	Visit  *store.VisitSummary
	HTML   template.HTML
	Stdout string
	Stderr string
	Feed   []FeedItem
	JSON   string
	Lanes  []*store.RunSnapshot
	Empty  string
}

func (d *Daemon) visitTab(id string, seq int, tab string) (*TabView, error) {
	s, err := d.eng.Snapshot(id)
	if err != nil {
		return nil, err
	}
	v := s.Visit(seq)
	if v == nil {
		return nil, &engine.Error{Kind: engine.KindNotFound, Msg: "visit not found"}
	}
	dir := filepath.Join(d.st.RunDir(id), store.VisitsDir, v.Dir)
	tv := &TabView{RunID: id, Seq: seq, Tab: tab, Type: v.Type, Visit: v}
	switch tab {
	case "input":
		b, err := os.ReadFile(filepath.Join(dir, "input.md"))
		if err != nil {
			tv.Empty = "Nothing rendered yet."
		} else {
			tv.HTML = Markdown(string(b))
		}
	case "handover":
		b, err := os.ReadFile(filepath.Join(dir, "handover.md"))
		if err != nil {
			tv.Empty = "The handover is written when the step finishes."
		} else {
			tv.HTML = Markdown(string(b))
		}
	case "result":
		b, err := os.ReadFile(filepath.Join(dir, "result.json"))
		if err != nil {
			tv.Empty = "No result yet."
		} else {
			tv.JSON = prettyJSON(b)
		}
	case "live":
		switch v.Type {
		case pipeline.TypeAgent, pipeline.TypeSplit:
			tv.Feed = transcriptFeed(filepath.Join(dir, "transcript.jsonl"))
			tv.Stderr = readTail(filepath.Join(dir, "stderr.log"), 16<<10)
		case pipeline.TypeFanout:
			kids, _ := d.runList("", "", id, 0)
			sort.Slice(kids, func(i, j int) bool { return kids[i].ID < kids[j].ID })
			tv.Lanes = kids
		default:
			tv.Stdout = readTail(filepath.Join(dir, "stdout.log"), 64<<10)
			tv.Stderr = readTail(filepath.Join(dir, "stderr.log"), 32<<10)
		}
	default:
		return nil, &engine.Error{Kind: engine.KindNotFound, Msg: "unknown tab"}
	}
	return tv, nil
}

// --- pipelines page ----------------------------------------------------------

// PipelineCard is one pipeline on the pipelines page.
type PipelineCard struct {
	PipelineInfo
	Repo      string
	FromBrief []VarField
	Errors    bool
}

// VarField is a start-form field for a from_brief variable.
type VarField struct {
	Name, Prompt, Format string
}

// PipelinesView is the pipelines page.
type PipelinesView struct {
	Repos  []PipelineRepo
	Global []PipelineCard
	Extra  string
}

// PipelineRepo is one repo section.
type PipelineRepo struct {
	Path  string
	Name  string
	Cards []PipelineCard
}

func cardsFor(repo string, infos []PipelineInfo, onlyGlobal *bool) []PipelineCard {
	var out []PipelineCard
	for _, pi := range infos {
		if onlyGlobal != nil && pi.Global != *onlyGlobal {
			continue
		}
		c := PipelineCard{PipelineInfo: pi, Repo: repo, Errors: pipeline.HasErrors(pi.Findings)}
		if pi.Pipeline != nil {
			for _, n := range pi.Pipeline.SortedVars() {
				if vr := pi.Pipeline.Variables[n]; vr != nil && vr.Source() == pipeline.SourceFromBrief {
					c.FromBrief = append(c.FromBrief, VarField{Name: n, Prompt: vr.Prompt, Format: vr.Format})
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// --- routes ------------------------------------------------------------------

func (d *Daemon) uiRoutes(mux *http.ServeMux) {
	static, _ := fs.Sub(web.Static, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	mux.HandleFunc("GET /{$}", d.pageRuns)
	mux.HandleFunc("GET /runs/{id}", d.pageRun)
	mux.HandleFunc("GET /inbox", d.pageInbox)
	mux.HandleFunc("GET /pipelines", d.pagePipelines)
	mux.HandleFunc("GET /fragments/runs", d.fragRuns)
	mux.HandleFunc("GET /fragments/inbox", d.fragInbox)
	mux.HandleFunc("GET /fragments/runs/{id}/{part}", d.fragRun)
	mux.HandleFunc("GET /fragments/runs/{id}/visits/{seq}/{tab}", d.fragTab)
}

type layoutData struct {
	Title  string
	Page   string
	Inbox  int
	Filter string
	Run    string // run id for the SSE filter
	Data   any
}

func (d *Daemon) layout(title, page string, data any) layoutData {
	return layoutData{Title: title, Page: page, Inbox: d.hub.InboxCount(), Data: data}
}

func filterOf(r *http.Request) string {
	switch f := r.URL.Query().Get("filter"); f {
	case "active", "inbox", "finished", "all":
		return f
	}
	return "active"
}

func (d *Daemon) pageRuns(w http.ResponseWriter, r *http.Request) {
	f := filterOf(r)
	groups, err := d.runGroups(f)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ld := d.layout("Runs", "runs", groups)
	ld.Filter = f
	d.render(w, "runs", "layout", ld)
}

func (d *Daemon) fragRuns(w http.ResponseWriter, r *http.Request) {
	f := filterOf(r)
	groups, err := d.runGroups(f)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d.render(w, "runs", "runs-list", map[string]any{"Groups": groups, "Filter": f})
}

func (d *Daemon) pageRun(w http.ResponseWriter, r *http.Request) {
	id, err := d.st.Resolve(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	v, err := d.runView(id)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	ld := d.layout(v.S.Title, "run", v)
	ld.Run = id
	d.render(w, "run", "layout", ld)
}

func (d *Daemon) fragRun(w http.ResponseWriter, r *http.Request) {
	id, err := d.st.Resolve(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	part := r.PathValue("part")
	switch part {
	case "header", "action", "timeline", "side":
	default:
		http.Error(w, "unknown fragment", 404)
		return
	}
	v, err := d.runView(id)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	d.render(w, "run", "run-"+part, v)
}

func (d *Daemon) fragTab(w http.ResponseWriter, r *http.Request) {
	id, err := d.st.Resolve(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	var seq int
	fmt.Sscan(r.PathValue("seq"), &seq)
	tv, err := d.visitTab(id, seq, r.PathValue("tab"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	d.render(w, "run", "visit-tab", tv)
}

func (d *Daemon) inboxItems() []engine.InboxItem {
	items, _ := d.eng.Inbox()
	return items
}

// InboxView pairs an inbox item with its run view, for inline answers.
type InboxView struct {
	Item engine.InboxItem
	V    *RunView
}

func (d *Daemon) inboxViews() []InboxView {
	var out []InboxView
	for _, it := range d.inboxItems() {
		v, err := d.runView(it.Run.ID)
		if err != nil {
			continue
		}
		out = append(out, InboxView{Item: it, V: v})
	}
	return out
}

func (d *Daemon) pageInbox(w http.ResponseWriter, r *http.Request) {
	d.render(w, "inbox", "layout", d.layout("Inbox", "inbox", d.inboxViews()))
}

func (d *Daemon) fragInbox(w http.ResponseWriter, r *http.Request) {
	d.render(w, "inbox", "inbox-list", d.inboxViews())
}

func (d *Daemon) pagePipelines(w http.ResponseWriter, r *http.Request) {
	view := PipelinesView{Extra: r.URL.Query().Get("repo")}
	repos := d.knownRepos()
	if view.Extra != "" {
		found := false
		for _, rp := range repos {
			found = found || rp == view.Extra
		}
		if !found {
			repos = append([]string{view.Extra}, repos...)
		}
	}
	notGlobal, global := false, true
	for _, repo := range repos {
		view.Repos = append(view.Repos, PipelineRepo{Path: repo, Name: filepath.Base(repo), Cards: cardsFor(repo, d.repoPipelines(repo), &notGlobal)})
	}
	view.Global = cardsFor("", d.repoPipelines(""), &global)
	d.render(w, "pipelines", "layout", d.layout("Pipelines", "pipelines", view))
}

// --- static assets -----------------------------------------------------------

// staticHandler serves embedded assets, gzipped when the client accepts it.
func staticHandler(fsys fs.FS) http.Handler {
	var mu sync.Mutex
	gz := map[string][]byte{}
	plain := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || !(strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css")) {
			plain.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		data, ok := gz[name]
		if !ok {
			raw, err := fs.ReadFile(fsys, name)
			if err != nil {
				mu.Unlock()
				http.NotFound(w, r)
				return
			}
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(raw)
			zw.Close()
			data = buf.Bytes()
			gz[name] = data
		}
		mu.Unlock()
		ct := "application/javascript"
		if strings.HasSuffix(name, ".css") {
			ct = "text/css"
		}
		w.Header().Set("Content-Type", ct+"; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Write(data)
	})
}
