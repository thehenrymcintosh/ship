package daemon

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

// Markdown renders the subset of markdown briefs and handovers use:
// headings, paragraphs, lists, fenced code, blockquotes, tables, rules,
// and inline code, bold, italics and links. Input is escaped first, so the
// output is safe.
func Markdown(src string) template.HTML {
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var para []string
	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	type list struct {
		ordered bool
		indent  int
	}
	var lists []list
	closeLists := func(toIndent int) {
		for len(lists) > 0 && lists[len(lists)-1].indent >= toIndent {
			if lists[len(lists)-1].ordered {
				b.WriteString("</li></ol>\n")
			} else {
				b.WriteString("</li></ul>\n")
			}
			lists = lists[:len(lists)-1]
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))

		// Fenced code.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flushPara()
			closeLists(0)
			fence := trimmed[:3]
			lang := strings.TrimSpace(trimmed[3:])
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
				code = append(code, lines[i])
			}
			cls := ""
			if lang != "" {
				cls = ` class="lang-` + html.EscapeString(lang) + `"`
			}
			b.WriteString("<pre><code" + cls + ">" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
			continue
		}
		if trimmed == "" {
			flushPara()
			// A blank line inside a list keeps it open if the next line continues it.
			if len(lists) > 0 && i+1 < len(lines) && listItem.MatchString(lines[i+1]) {
				continue
			}
			closeLists(0)
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") && strings.HasSuffix(trimmed, "-->") {
			continue
		}
		if m := heading.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			closeLists(0)
			n := len(m[1])
			b.WriteString("<h" + string(rune('0'+n)) + ">" + inline(m[2]) + "</h" + string(rune('0'+n)) + ">\n")
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushPara()
			closeLists(0)
			b.WriteString("<hr>\n")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushPara()
			closeLists(0)
			var q []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				q = append(q, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"), " "))
			}
			i--
			b.WriteString("<blockquote>" + string(Markdown(strings.Join(q, "\n"))) + "</blockquote>\n")
			continue
		}
		if strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && tableSep.MatchString(strings.TrimSpace(lines[i+1])) {
			flushPara()
			closeLists(0)
			b.WriteString("<table><thead><tr>")
			for _, c := range cells(trimmed) {
				b.WriteString("<th>" + inline(c) + "</th>")
			}
			b.WriteString("</tr></thead><tbody>\n")
			for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				b.WriteString("<tr>")
				for _, c := range cells(strings.TrimSpace(lines[i])) {
					b.WriteString("<td>" + inline(c) + "</td>")
				}
				b.WriteString("</tr>\n")
			}
			i--
			b.WriteString("</tbody></table>\n")
			continue
		}
		if m := listItem.FindStringSubmatch(line); m != nil {
			flushPara()
			ordered := m[2] != "-" && m[2] != "*" && m[2] != "+"
			switch {
			case len(lists) == 0 || indent > lists[len(lists)-1].indent:
				lists = append(lists, list{ordered, indent})
				if ordered {
					b.WriteString("<ol>")
				} else {
					b.WriteString("<ul>")
				}
			default:
				closeLists(indent + 1)
				if len(lists) > 0 && lists[len(lists)-1].ordered != ordered {
					// Switching between - and 1. at the same depth starts a new list.
					closeLists(lists[len(lists)-1].indent)
				}
				if len(lists) == 0 {
					lists = append(lists, list{ordered, indent})
					if ordered {
						b.WriteString("<ol>")
					} else {
						b.WriteString("<ul>")
					}
				} else {
					b.WriteString("</li>")
				}
			}
			text := m[3]
			if strings.HasPrefix(text, "[ ] ") {
				text = "☐ " + text[4:]
			} else if strings.HasPrefix(text, "[x] ") || strings.HasPrefix(text, "[X] ") {
				text = "☑ " + text[4:]
			}
			b.WriteString("<li>" + inline(text))
			continue
		}
		if len(lists) > 0 && indent > 0 {
			// Continuation of a list item.
			b.WriteString(" " + inline(trimmed))
			continue
		}
		closeLists(0)
		para = append(para, trimmed)
	}
	flushPara()
	closeLists(0)
	return template.HTML(b.String())
}

var (
	heading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	listItem = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	tableSep = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
	codeSpan = regexp.MustCompile("`([^`]+)`")
	bold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italic   = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*)\*`)
	link     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	autolink = regexp.MustCompile(`(^|[\s(])(https?://[^\s<)]+)`)
)

func cells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	parts := strings.Split(row, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// inline escapes s and renders inline markup. Code spans are protected from
// the other rules.
func inline(s string) string {
	var spans []string
	s = codeSpan.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return "\x00" + string(rune('A'+len(spans)-1)) + "\x00"
	})
	s = html.EscapeString(s)
	s = link.ReplaceAllStringFunc(s, func(m string) string {
		p := link.FindStringSubmatch(m)
		href := p[2]
		if !safeHref(href) {
			return p[1]
		}
		return `<a href="` + href + `" target="_blank" rel="noopener">` + p[1] + `</a>`
	})
	s = autolink.ReplaceAllString(s, `$1<a href="$2" target="_blank" rel="noopener">$2</a>`)
	s = bold.ReplaceAllString(s, "<strong>$1</strong>")
	s = italic.ReplaceAllString(s, "$1<em>$2</em>")
	for i, sp := range spans {
		s = strings.Replace(s, "\x00"+string(rune('A'+i))+"\x00", sp, 1)
	}
	return s
}

func safeHref(h string) bool {
	l := strings.ToLower(h)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "/") || strings.HasPrefix(l, "#")
}
