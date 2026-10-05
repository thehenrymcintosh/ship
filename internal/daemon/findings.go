package daemon

import (
	"html/template"
	"regexp"
	"strings"
)

// Finding is one review finding picked out of a handover, written as
// "R1 [warning, auto-fix] path:line: what's wrong" plus the lines under it.
type Finding struct {
	ID       string
	Severity string
	Action   string
	Level    string        // danger | warn | info, for the colour
	Where    template.HTML // file:line part of the title, if any
	Title    template.HTML
	Body     template.HTML
	NeedsYou bool // the reviewer left it to the person (ask-user)
}

// HandoverPart is a run of plain markdown or one finding.
type HandoverPart struct {
	HTML    template.HTML
	Finding *Finding
}

var (
	findingRE = regexp.MustCompile(`^\s*(?:[-*]\s+)?(?:#{1,6}\s+)?(?:\*\*)?([A-Z]{1,3}[0-9]{1,3})(?:\*\*)?:?\s*\[([^\],]+)(?:,\s*([^\]]+))?\](?:\*\*)?:?\s*(.*)$`)
	whereRE   = regexp.MustCompile(`[\w./-]+:[0-9]`)
	headingRE = regexp.MustCompile(`^\s{0,3}#{1,6}\s`)
	listRE    = regexp.MustCompile(`^(\s+\S|\s*([-*+]|[0-9]+[.)])\s)`)
)

// splitFindings cuts a handover into markdown and findings. It returns nil
// when there are no findings, so callers can render it as plain markdown.
func splitFindings(src string) []HandoverPart {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var parts []HandoverPart
	var plain []string
	found, fence := false, false
	flush := func() {
		if strings.TrimSpace(strings.Join(plain, "")) != "" {
			parts = append(parts, HandoverPart{HTML: Markdown(strings.Join(plain, "\n"))})
		}
		plain = nil
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
		}
		m := findingRE.FindStringSubmatch(line)
		if fence || m == nil {
			plain = append(plain, line)
			continue
		}
		flush()
		found = true
		f := &Finding{ID: m[1], Severity: strings.ToLower(strings.TrimSpace(m[2])), Action: strings.ToLower(strings.TrimSpace(m[3]))}
		f.Level = severityLevel(f.Severity)
		f.NeedsYou = strings.Contains(f.Action, "ask") || strings.Contains(f.Action, "user") || strings.Contains(f.Action, "decide")
		title := strings.TrimSpace(m[4])
		if k := strings.Index(title, ": "); k > 0 && whereRE.MatchString(title[:k]) {
			f.Where, title = template.HTML(inline(title[:k])), title[k+2:]
		}
		f.Title = template.HTML(inline(title))
		// The body runs to the next finding or heading, or a blank line
		// that isn't followed by more of its list.
		var body []string
		for i+1 < len(lines) {
			next := lines[i+1]
			if findingRE.MatchString(next) || headingRE.MatchString(next) {
				break
			}
			if strings.TrimSpace(next) == "" {
				if i+2 >= len(lines) || !listRE.MatchString(lines[i+2]) || findingRE.MatchString(lines[i+2]) {
					break
				}
			}
			body = append(body, next)
			i++
		}
		if len(body) > 0 {
			f.Body = Markdown(strings.Join(body, "\n"))
		}
		parts = append(parts, HandoverPart{Finding: f})
	}
	flush()
	if !found {
		return nil
	}
	return parts
}

func severityLevel(s string) string {
	switch s {
	case "critical", "blocker", "error", "high", "major", "severe", "bug":
		return "danger"
	case "warning", "warn", "medium", "moderate":
		return "warn"
	}
	return "info"
}
