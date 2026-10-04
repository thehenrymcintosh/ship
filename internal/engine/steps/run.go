package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/merlin-digital/ship/internal/pipeline"
	"github.com/merlin-digital/ship/internal/proc"
	"github.com/merlin-digital/ship/internal/tmpl"
)

// maxCapture caps how much stdout is kept in memory for `save`.
const maxCapture = 16 << 20

// Run executes `run` steps.
type Run struct{}

// Execute renders the script and runs it with bash.
func (Run) Execute(ctx context.Context, v *Visit) (Result, error) {
	script, err := tmpl.Render(pipeline.Str(v.Step.Run), v.Scope, tmpl.Shell)
	if err != nil {
		return renderError(err), nil
	}
	_ = writeFile(v.File("input.md"), "```bash\n"+script+"\n```\n")

	res, out, failed := runScript(ctx, v, script, false)
	if failed != nil {
		return *failed, nil
	}
	outcome := v.Step.RunOutcome(res.ExitCode)
	r := Result{Outcome: outcome, ExitCode: intPtr(res.ExitCode), Output: out.tail.Lines()}
	r.Summary = firstLine(v.Step.Description)
	if r.Summary == "" {
		r.Summary = fmt.Sprintf("exit %d", res.ExitCode)
	} else {
		r.Summary += fmt.Sprintf(" (exit %d)", res.ExitCode)
	}
	if v.Step.Save != nil && (outcome == "pass" || v.Step.SaveOn == "any") {
		vars, serr := saveVars(v, out.stdout.Bytes())
		if serr != nil {
			e := ErrorResult("save", serr.Error())
			e.ExitCode, e.Output = r.ExitCode, r.Output
			return e, nil
		}
		r.Vars = vars
	}
	return r, nil
}

type captured struct {
	stdout bytes.Buffer
	tail   *proc.Tail
}

// runScript runs bash with the visit's env and logs. It returns a non-nil
// *Result for start failures and timeouts.
func runScript(ctx context.Context, v *Visit, script string, appendLogs bool) (proc.Result, *captured, *Result) {
	c := &captured{tail: proc.NewTail(v.OutputTail)}
	so, err := openLog(v.File("stdout.log"), appendLogs)
	if err != nil {
		r := ErrorResult("start", err.Error())
		return proc.Result{}, c, &r
	}
	defer so.Close()
	se, err := openLog(v.File("stderr.log"), appendLogs)
	if err != nil {
		r := ErrorResult("start", err.Error())
		return proc.Result{}, c, &r
	}
	defer se.Close()

	stdout := io.MultiWriter(so, c.tail, limitWriter{&c.stdout, maxCapture}, proc.FuncWriter(func(p []byte) { v.RT.Output("stdout", p) }))
	stderr := io.MultiWriter(se, c.tail, proc.FuncWriter(func(p []byte) { v.RT.Output("stderr", p) }))

	runCtx := ctx
	if v.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, v.Timeout)
		defer cancel()
	}
	res, err := proc.Run(runCtx, proc.Spec{
		Path: "bash", Args: []string{"-euo", "pipefail", "-c", script},
		Dir: v.Worktree, Env: v.Env, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		r := ErrorResult("start", err.Error())
		return res, c, &r
	}
	if ctx.Err() != nil {
		r := Result{Outcome: OutcomeCancelled, Summary: "cancelled"}
		return res, c, &r
	}
	if runCtx.Err() == context.DeadlineExceeded {
		r := ErrorResult("timeout", fmt.Sprintf("timed out after %s", v.Timeout))
		r.Output = c.tail.Lines()
		return res, c, &r
	}
	return res, c, nil
}

type limitWriter struct {
	b   *bytes.Buffer
	max int
}

func (w limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		if len(p) > room {
			w.b.Write(p[:room])
		} else {
			w.b.Write(p)
		}
	}
	return len(p), nil
}

func saveVars(v *Visit, stdout []byte) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range v.Step.Save.Sources {
		val, err := saveSource(v, kv.Value, stdout)
		if err != nil {
			return nil, fmt.Errorf("save %s (%s): %w", kv.Key, kv.Value, err)
		}
		out[kv.Key] = val
	}
	return out, nil
}

func saveSource(v *Visit, src string, stdout []byte) (string, error) {
	switch {
	case src == "last_line":
		lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(lines[i]); s != "" {
				return s, nil
			}
		}
		return "", errors.New("stdout was empty")
	case src == "stdout":
		return strings.TrimSpace(string(stdout)), nil
	case strings.HasPrefix(src, "file:"):
		p := strings.TrimPrefix(src, "file:")
		if !filepath.IsAbs(p) {
			p = filepath.Join(v.Worktree, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	case strings.HasPrefix(src, "json:"):
		var doc any
		if err := json.Unmarshal(stdout, &doc); err != nil {
			return "", fmt.Errorf("stdout is not JSON: %v", err)
		}
		return JSONPath(doc, strings.TrimPrefix(src, "json:"))
	}
	return "", fmt.Errorf("unknown save source %q", src)
}

// JSONPath follows a dotted path (map keys and array indexes).
func JSONPath(doc any, path string) (string, error) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		switch c := cur.(type) {
		case map[string]any:
			next, ok := c[seg]
			if !ok {
				return "", fmt.Errorf("no key %q", seg)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(c) {
				return "", fmt.Errorf("bad index %q", seg)
			}
			cur = c[i]
		default:
			return "", fmt.Errorf("can't descend into %q", seg)
		}
	}
	switch c := cur.(type) {
	case string:
		return c, nil
	case nil:
		return "", nil
	}
	b, _ := json.Marshal(cur)
	return string(b), nil
}

func renderError(err error) Result {
	var unset *tmpl.UnsetVarError
	if errors.As(err, &unset) {
		return ErrorResult("unset_var:"+unset.Name, err.Error())
	}
	return ErrorResult("template", err.Error())
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
