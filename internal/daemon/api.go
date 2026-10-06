package daemon

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thehenrymcintosh/ship/internal/brand"
	"github.com/thehenrymcintosh/ship/internal/engine"
	"github.com/thehenrymcintosh/ship/internal/history"
	"github.com/thehenrymcintosh/ship/internal/pipeline"
	"github.com/thehenrymcintosh/ship/internal/store"
)

// routes builds the HTTP handler.
func (d *Daemon) routes() http.Handler {
	mux := http.NewServeMux()
	api := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }

	api("GET /api/health", d.health)
	api("GET /api/runs", d.listRuns)
	api("POST /api/runs", d.startRun)
	api("GET /api/runs/{id}", d.getRun)
	api("GET /api/runs/{id}/events", d.runEvents)
	api("GET /api/runs/{id}/visits/{seq}", d.getVisit)
	api("GET /api/runs/{id}/visits/{seq}/files/{name...}", d.visitFile)
	api("GET /api/runs/{id}/brief", d.runBrief)
	api("GET /api/runs/{id}/graph", d.runGraph)
	api("POST /api/runs/{id}/answer", d.command(engine.CmdAnswer))
	api("POST /api/runs/{id}/split-review", d.command(engine.CmdSplitReview))
	api("POST /api/runs/{id}/retry", d.command(engine.CmdRetry))
	api("POST /api/runs/{id}/goto", d.command(engine.CmdGoto))
	api("POST /api/runs/{id}/resume-session", d.command(engine.CmdResumeSession))
	api("POST /api/runs/{id}/vars", d.command(engine.CmdSetVar))
	api("POST /api/runs/{id}/cancel", d.command(engine.CmdCancel))
	api("POST /api/runs/{id}/pause", d.command(engine.CmdPause))
	api("POST /api/runs/{id}/resume", d.command(engine.CmdResume))
	api("POST /api/runs/{id}/pr/trigger", d.command(engine.CmdPRTrigger))
	api("POST /api/runs/{id}/reacquire", d.command(engine.CmdReacquire))
	api("POST /api/runs/{id}/upgrade", d.upgrade)
	api("POST /api/runs/{id}/budget", d.command(engine.CmdRaiseBudget))
	api("POST /api/runs/{id}/focus", d.focus)
	api("GET /api/usage", d.getUsage)
	api("POST /api/runs/{id}/open", d.openThing)
	api("GET /api/runs/{id}/feedback", d.runFeedback)
	api("POST /api/runs/{id}/feedback", d.addFeedback)
	api("GET /api/inbox", d.inbox)
	api("GET /api/repos", d.repos)
	api("GET /api/pipelines", d.pipelines)
	api("GET /api/pipelines/{name}/graph", d.pipelineGraph)
	api("POST /api/pipelines/{name}/backfill", d.backfillStats)
	api("GET /api/events", d.events)
	api("POST /api/shutdown", d.shutdown)
	api("POST /api/clean", d.clean)
	api("POST /api/prune", d.prune)
	d.uiRoutes(mux)
	return d.guard(mux)
}

// --- auth ------------------------------------------------------------

func (d *Daemon) originOK(origin string) bool {
	if origin == "" {
		return true
	}
	p := strconv.Itoa(d.info.Port)
	return origin == "http://127.0.0.1:"+p || origin == "http://localhost:"+p
}

func (d *Daemon) tokenOK(r *http.Request) bool {
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimPrefix(h, "Bearer ")
	} else if c, err := r.Cookie(brand.CookieName); err == nil {
		tok = c.Value
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(d.info.Token)) == 1
}

func (d *Daemon) guard(next http.Handler) http.Handler {
	p := strconv.Itoa(d.info.Port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DNS-rebinding defence: only our own host names.
		if r.Host != "127.0.0.1:"+p && r.Host != "localhost:"+p {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/health" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		// Browser login: /?t=<token>[&next=/runs/x] sets the cookie and strips the token.
		if t := r.URL.Query().Get("t"); t != "" && r.Method == http.MethodGet {
			if subtle.ConstantTimeCompare([]byte(t), []byte(d.info.Token)) != 1 {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: brand.CookieName, Value: d.info.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			dest := r.URL.Query().Get("next")
			if !strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "//") {
				dest = r.URL.Path
			}
			http.Redirect(w, r, dest, http.StatusSeeOther)
			return
		}
		if !d.tokenOK(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title><body style="font-family:system-ui;padding:3rem;max-width:36rem">
<h1>Not signed in</h1><p>Open the UI with <code>%s open</code>: it signs this browser in with the daemon's token.</p>`, brand.Name, brand.Name)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !d.originOK(r.Header.Get("Origin")) {
			writeErr(w, http.StatusForbidden, "bad_origin", "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- helpers -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// APIError is the error body shape.
type APIError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Errors []pipeline.Finding `json:"errors,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var e APIError
	e.Error.Code, e.Error.Message = code, msg
	writeJSON(w, status, e)
}

func writeEngineErr(w http.ResponseWriter, err error) {
	var ee *engine.Error
	var amb *store.AmbiguousError
	switch {
	case errors.As(err, &ee):
		var e APIError
		e.Error.Message, e.Errors = ee.Msg, ee.Findings
		status := http.StatusBadRequest
		switch ee.Kind {
		case engine.KindInvalid:
			status, e.Error.Code = http.StatusUnprocessableEntity, "invalid"
		case engine.KindConflict:
			status, e.Error.Code = http.StatusConflict, "conflict"
		case engine.KindNotFound:
			status, e.Error.Code = http.StatusNotFound, "not_found"
		}
		writeJSON(w, status, e)
	case errors.As(err, &amb):
		writeErr(w, http.StatusBadRequest, "ambiguous", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// decode reads a JSON or form body into v (a struct with json tags).
func decode(r *http.Request, v any) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "application/x-www-form-urlencoded" || ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return err
		}
		m := map[string]any{}
		for k, vals := range r.Form {
			if len(vals) == 0 {
				continue
			}
			if strings.HasPrefix(k, "vars.") {
				vm, _ := m["vars"].(map[string]any)
				if vm == nil {
					vm = map[string]any{}
					m["vars"] = vm
				}
				vm[strings.TrimPrefix(k, "vars.")] = vals[0]
				continue
			}
			switch vals[0] {
			case "true", "on":
				m[k] = true
			default:
				m[k] = vals[0]
			}
		}
		if r.MultipartForm != nil {
			if fhs := r.MultipartForm.File["brief_file"]; len(fhs) > 0 {
				f, err := fhs[0].Open()
				if err == nil {
					b, _ := io.ReadAll(io.LimitReader(f, 4<<20))
					f.Close()
					if len(strings.TrimSpace(string(b))) > 0 {
						m["brief"] = string(b)
					}
				}
			}
		}
		b, _ := json.Marshal(m)
		return json.Unmarshal(b, v)
	}
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(v)
}

func (d *Daemon) resolve(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := d.st.Resolve(r.PathValue("id"))
	if err != nil {
		writeEngineErr(w, err)
		return "", false
	}
	return id, true
}

// --- handlers --------------------------------------------------------------

func (d *Daemon) health(w http.ResponseWriter, r *http.Request) {
	agents := map[string]string{}
	d.agentVers.Range(func(k, v any) bool { agents[k.(string)] = v.(string); return true })
	writeJSON(w, 200, map[string]any{
		"version": brand.Version, "pid": d.info.PID,
		"uptime": time.Since(d.started).Round(time.Second).String(), "agents": agents,
	})
}

func statusMatches(filter string, s *store.RunSnapshot) bool {
	switch filter {
	case "", "all":
		return true
	case "active":
		return !s.Status.Terminal()
	case "inbox":
		return s.Status.InInbox()
	case "finished":
		return s.Status.Terminal()
	}
	for _, f := range strings.Split(filter, ",") {
		if string(s.Status) == f {
			return true
		}
	}
	return false
}

// RunList returns snapshots (live where active), newest first.
func (d *Daemon) runList(status, repo, parent string, limit int) ([]*store.RunSnapshot, error) {
	snaps, err := d.st.List()
	if err != nil {
		return nil, err
	}
	var out []*store.RunSnapshot
	for _, s := range snaps {
		if live, err := d.eng.Snapshot(s.ID); err == nil {
			s = live
		}
		if !statusMatches(status, s) || repo != "" && s.Repo != repo {
			continue
		}
		switch parent {
		case "":
		case "none":
			if s.Parent != nil {
				continue
			}
		default:
			if s.Parent == nil || s.Parent.ID != parent {
				continue
			}
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (d *Daemon) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	snaps, err := d.runList(q.Get("status"), q.Get("repo"), q.Get("parent"), limit)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	out := make([]store.RunSnapshot, len(snaps))
	for i, s := range snaps {
		out[i] = s.Lite()
	}
	writeJSON(w, 200, out)
}

// StartBody is POST /api/runs.
type StartBody struct {
	Repo       string            `json:"repo"`
	Pipeline   string            `json:"pipeline"`
	Brief      string            `json:"brief"`
	Vars       map[string]string `json:"vars"`
	FakeAgents string            `json:"fake_agents,omitempty"`
}

func (d *Daemon) startRun(w http.ResponseWriter, r *http.Request) {
	var body StartBody
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	for k, v := range body.Vars {
		if strings.TrimSpace(v) == "" {
			delete(body.Vars, k)
		}
	}
	snap, err := d.eng.Start(r.Context(), engine.StartRequest{Repo: body.Repo, Pipeline: body.Pipeline, Brief: []byte(body.Brief), Vars: body.Vars, FakeAgents: body.FakeAgents})
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	u := d.info.URL() + "/runs/" + url.PathEscape(snap.ID)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/runs/"+url.PathEscape(snap.ID))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": snap.ID, "url": u, "warnings": snap.Warnings})
}

func (d *Daemon) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	snap, err := d.eng.Snapshot(id)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, 200, snap)
}

func (d *Daemon) runEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	evs, _, err := store.ReadEvents(d.st.RunDir(id))
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	out := []store.Event{}
	for _, e := range evs {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	writeJSON(w, 200, out)
}

func (d *Daemon) visitDir(id, seqStr string) (string, *store.VisitSummary, error) {
	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		return "", nil, &engine.Error{Kind: engine.KindNotFound, Msg: "bad visit seq"}
	}
	snap, err := d.eng.Snapshot(id)
	if err != nil {
		return "", nil, err
	}
	v := snap.Visit(seq)
	if v == nil {
		return "", nil, &engine.Error{Kind: engine.KindNotFound, Msg: fmt.Sprintf("visit %d not found", seq)}
	}
	return filepath.Join(d.st.RunDir(id), store.VisitsDir, v.Dir), v, nil
}

func (d *Daemon) getVisit(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	dir, v, err := d.visitDir(id, r.PathValue("seq"))
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	var result json.RawMessage
	if b, err := os.ReadFile(filepath.Join(dir, "result.json")); err == nil {
		result = b
	}
	files := []string{}
	for _, name := range visitFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			files = append(files, name)
		}
	}
	slices, _ := filepath.Glob(filepath.Join(dir, "slices", "*.md"))
	for _, s := range slices {
		files = append(files, "slices/"+filepath.Base(s))
	}
	writeJSON(w, 200, map[string]any{"visit": v, "result": result, "dir": dir, "files": files})
}

var visitFiles = []string{"input.md", "stdout.log", "stderr.log", "transcript.jsonl", "handover.md", "result.json"}

func allowedVisitFile(name string) bool {
	for _, f := range visitFiles {
		if name == f {
			return true
		}
	}
	if strings.HasPrefix(name, "slices/") {
		base := strings.TrimPrefix(name, "slices/")
		return base != "" && !strings.ContainsAny(base, `/\`) && strings.HasSuffix(base, ".md") && !strings.HasPrefix(base, ".")
	}
	return false
}

func (d *Daemon) visitFile(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !allowedVisitFile(name) {
		writeErr(w, 404, "not_found", "no such visit file")
		return
	}
	dir, _, err := d.visitDir(id, r.PathValue("seq"))
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		writeErr(w, 404, "not_found", "file not written yet")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
	if offset < 0 {
		// Negative offset: the last -offset bytes.
		offset = st.Size() + offset
		if offset < 0 {
			offset = 0
		}
	}
	if offset > 0 {
		f.Seek(offset, io.SeekStart)
	}
	var rd io.Reader = f
	if limit > 0 {
		rd = io.LimitReader(f, limit)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-File-Size", strconv.FormatInt(st.Size(), 10))
	io.Copy(w, rd)
}

func (d *Daemon) runBrief(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	b, err := os.ReadFile(filepath.Join(d.st.RunDir(id), store.BriefFile))
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write(b)
}

func (d *Daemon) runGraph(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	g, err := d.eng.Graph(id)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, 200, g)
}

// CommandBody is the body of the control endpoints.
type CommandBody struct {
	Choice string `json:"choice"`
	Note   string `json:"note"`
	For    string `json:"for"` // answer: who the note is for, "step" (default) or "run"
	Action string `json:"action"`
	Step   string `json:"step"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	All    bool   `json:"all"`
	Source string `json:"source"`
	// raise-budget: amounts to add ("5", "2.50"; "100k", "1m")
	USD    string `json:"usd"`
	Tokens string `json:"tokens"`
}

func (d *Daemon) command(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := d.resolve(w, r)
		if !ok {
			return
		}
		var b CommandBody
		if err := decode(r, &b); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		src := b.Source
		if src == "" {
			src = "ui"
			if r.Header.Get("X-Ship-Client") == "cli" {
				src = "cli"
			}
		}
		c := engine.Command{Name: name, Choice: b.Choice, Note: b.Note, For: b.For, Action: b.Action, Step: b.Step, Var: b.Name, Value: b.Value, All: b.All, Source: src}
		if usd := strings.TrimPrefix(strings.TrimSpace(b.USD), "$"); usd != "" {
			v, err := strconv.ParseFloat(usd, 64)
			if err != nil || v <= 0 {
				writeErr(w, 400, "bad_request", fmt.Sprintf("invalid amount %q", b.USD))
				return
			}
			c.USD = v
		}
		if t := strings.TrimSpace(b.Tokens); t != "" {
			n, err := pipeline.ParseTokens(t)
			if err != nil {
				writeErr(w, 400, "bad_request", err.Error())
				return
			}
			c.Tokens = n
		}
		if err := d.eng.Do(id, c); err != nil {
			writeEngineErr(w, err)
			return
		}
		// Give the run goroutine a moment so the returned snapshot reflects it.
		time.Sleep(30 * time.Millisecond)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Trigger", "ship-refresh")
		}
		snap, err := d.eng.Snapshot(id)
		if err != nil {
			writeEngineErr(w, err)
			return
		}
		writeJSON(w, 200, snap)
	}
}

// upgrade moves a run onto its pipeline as it is now; body {step}.
func (d *Daemon) upgrade(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	var b CommandBody
	if err := decode(r, &b); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	src := "ui"
	if r.Header.Get("X-Ship-Client") == "cli" {
		src = "cli"
	}
	if _, err := d.eng.Upgrade(r.Context(), id, b.Step, src); err != nil {
		writeEngineErr(w, err)
		return
	}
	time.Sleep(30 * time.Millisecond)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "ship-refresh")
	}
	snap, err := d.eng.Snapshot(id)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, 200, snap)
}

func (d *Daemon) openThing(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	var b struct {
		What string `json:"what"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	snap, err := d.eng.Snapshot(id)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	cfg, _ := d.eng.Config(snap.Repo)
	path := ""
	if snap.Workspace != nil {
		path = snap.Workspace.Path
	}
	var cmd *exec.Cmd
	switch b.What {
	case "worktree":
		cmd = exec.Command("open", path)
	case "editor":
		ed := cfg.UI.Editor
		if ed == "" {
			ed = firstEnv("VISUAL", "EDITOR")
		}
		if ed == "" {
			cmd = exec.Command("open", path)
		} else {
			cmd = exec.Command("sh", "-c", ed+` "$1"`, "sh", path)
		}
	case "terminal":
		if cfg.UI.Terminal != "" {
			cmd = exec.Command("sh", "-c", cfg.UI.Terminal+` "$1"`, "sh", path)
		} else {
			cmd = exec.Command("open", "-a", "Terminal", path)
		}
	case "slices":
		if lv := lastSplitVisit(snap); lv != nil {
			cmd = exec.Command("open", filepath.Join(d.st.RunDir(id), store.VisitsDir, lv.Dir, "slices"))
		}
	case "run_dir":
		cmd = exec.Command("open", d.st.RunDir(id))
		path = d.st.RunDir(id)
	}
	if cmd == nil || path == "" && b.What != "slices" {
		writeErr(w, 409, "conflict", "nothing to open")
		return
	}
	if err := cmd.Start(); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	go cmd.Wait()
	w.WriteHeader(http.StatusNoContent)
}

func lastSplitVisit(s *store.RunSnapshot) *store.VisitSummary {
	for i := len(s.Visits) - 1; i >= 0; i-- {
		if s.Visits[i].Type == pipeline.TypeSplit {
			return &s.Visits[i]
		}
	}
	return nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func (d *Daemon) inbox(w http.ResponseWriter, r *http.Request) {
	items, err := d.eng.Inbox()
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	type row struct {
		Run      store.RunSnapshot `json:"run"`
		Kind     string            `json:"kind"`
		Question string            `json:"question"`
		Since    time.Time         `json:"since"`
	}
	out := []row{}
	for _, it := range items {
		out = append(out, row{Run: it.Run.Lite(), Kind: it.Kind, Question: it.Question, Since: it.Since})
	}
	writeJSON(w, 200, out)
}

// RepoInfo is one repo seen in runs.
type RepoInfo struct {
	Path      string         `json:"path"`
	Pipelines []PipelineInfo `json:"pipelines"`
}

// PipelineInfo describes a pipeline file and its findings.
type PipelineInfo struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Findings    []pipeline.Finding `json:"findings"`
	Global      bool               `json:"global"` // from ~/.ship/pipelines
	Pipeline    *pipeline.Pipeline `json:"-"`
}

func (d *Daemon) knownRepos() []string {
	seen := map[string]bool{}
	var out []string
	snaps, _ := d.st.List()
	for _, s := range snaps {
		if s.Repo != "" && !seen[s.Repo] {
			seen[s.Repo] = true
			out = append(out, s.Repo)
		}
	}
	sort.Strings(out)
	return out
}

func (d *Daemon) repoPipelines(repo string) []PipelineInfo {
	cfg, _ := d.eng.Config(repo)
	l := d.eng.Loader(repo)
	out := []PipelineInfo{}
	opts := d.eng.ValidateOptions(cfg)
	opts.SkillExists = d.eng.SkillCheck(repo)
	for _, name := range l.Names() {
		f, findings := l.Validate(name, opts)
		pi := PipelineInfo{Name: name, Findings: findings, Global: l.Dir(name) != engine.PipelinesDir(repo)}
		if pi.Findings == nil {
			pi.Findings = []pipeline.Finding{}
		}
		if f != nil {
			pi.Description = f.Pipeline.Description
			pi.Pipeline = f.Pipeline
		}
		out = append(out, pi)
	}
	return out
}

func (d *Daemon) repos(w http.ResponseWriter, r *http.Request) {
	out := []RepoInfo{}
	for _, repo := range d.knownRepos() {
		out = append(out, RepoInfo{Path: repo, Pipelines: d.repoPipelines(repo)})
	}
	writeJSON(w, 200, out)
}

func (d *Daemon) pipelines(w http.ResponseWriter, r *http.Request) {
	// Without ?repo= this lists the global pipelines only.
	writeJSON(w, 200, d.repoPipelines(r.URL.Query().Get("repo")))
}

func (d *Daemon) pipelineGraph(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	f, findings := d.eng.Loader(repo).Load(r.PathValue("name"))
	if f == nil {
		msg := "pipeline not found"
		if len(findings) > 0 {
			msg = findings[0].String()
		}
		writeErr(w, 404, "not_found", msg)
		return
	}
	writeJSON(w, 200, f.Pipeline.Graph())
}

func (d *Daemon) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "internal", "streaming unsupported")
		return
	}
	run := r.URL.Query().Get("run")
	if run != "" {
		if id, err := d.st.Resolve(run); err == nil {
			run = id
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	sub := d.hub.subscribe(run)
	defer d.hub.unsubscribe(sub)
	w.Write(frame("inbox", map[string]int{"count": d.hub.InboxCount()}))
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-sub.ch:
			w.Write(msg)
			// Drain whatever else is queued before flushing.
			for n := 0; n < 64; n++ {
				select {
				case m := <-sub.ch:
					w.Write(m)
					continue
				default:
				}
				break
			}
			fl.Flush()
		case <-tick.C:
			io.WriteString(w, ": heartbeat\n\n")
			fl.Flush()
		}
	}
}

func (d *Daemon) shutdown(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Force   bool `json:"force"`
		Restart bool `json:"restart"`
	}
	_ = decode(r, &b)
	select {
	case d.stopCh <- stopReq{force: b.Force, wait: b.Restart}:
	default:
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"executing": d.eng.Executing()})
}

func (d *Daemon) clean(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Run    string `json:"run"`
		Force  bool   `json:"force"`
		DryRun bool   `json:"dry_run"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if b.Run != "" {
		id, err := d.st.Resolve(b.Run)
		if err != nil {
			writeEngineErr(w, err)
			return
		}
		b.Run = id
	}
	res, err := d.eng.Clean(r.Context(), engine.CleanOptions{RunID: b.Run, Force: b.Force, DryRun: b.DryRun})
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (d *Daemon) prune(w http.ResponseWriter, r *http.Request) {
	var b struct {
		OlderThan string `json:"older_than"` // e.g. 30d; default retention.keep_runs_days
		Force     bool   `json:"force"`
		DryRun    bool   `json:"dry_run"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	o := engine.PruneOptions{Force: b.Force, DryRun: b.DryRun}
	if b.OlderThan != "" {
		d, err := pipeline.ParseDuration(b.OlderThan)
		if err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		o.OlderThan = d
	} else {
		// Deleting can't be undone, so a config that doesn't load must not
		// fall back to a zero cutoff.
		cfg, err := d.eng.Config("")
		if err != nil {
			writeErr(w, 500, "config", "can't read the config, so the default age for prune is unknown: "+err.Error())
			return
		}
		if cfg.Retention.KeepRunsDays <= 0 {
			writeJSON(w, 200, engine.PruneResult{Pruned: []engine.PrunedRun{}, Note: "retention.keep_runs_days is 0, so nothing is pruned by default. Pass --older-than to choose an age."})
			return
		}
		o.OlderThan = time.Duration(cfg.Retention.KeepRunsDays) * 24 * time.Hour
	}
	res, err := d.eng.Prune(r.Context(), o)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "ship-refresh")
	}
	writeJSON(w, 200, res)
}

func (d *Daemon) runFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	snap, err := d.eng.Snapshot(id)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	items, err := engine.FeedbackForRun(snap, d.home)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if items == nil {
		items = []history.Item{}
	}
	writeJSON(w, 200, items)
}

func (d *Daemon) addFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := d.resolve(w, r)
	if !ok {
		return
	}
	var b struct {
		Step   string `json:"step"`
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	src := b.Source
	if src == "" {
		src = history.FromUI
		if r.Header.Get("X-Ship-Client") == "cli" {
			src = history.FromCLI
		}
	}
	f, err := d.eng.AddFeedback(id, b.Step, b.Text, src, "", "", "")
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "ship-refresh")
	}
	writeJSON(w, http.StatusCreated, f)
}
