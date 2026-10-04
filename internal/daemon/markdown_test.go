package daemon

import (
	"strings"
	"testing"
)

func TestMarkdown(t *testing.T) {
	src := "# Handover: review\n\n**Run:** x · `a<b>`\n\n## Summary\nSome *text* with [link](https://e.com) and [bad](javascript:alert(1)).\n\n- one\n- two\n  - nested\n1. first\n\n```go\nfmt.Println(\"<hi>\")\n```\n\n| a | b |\n|---|---|\n| 1 | `2` |\n\n<script>alert(1)</script>\n"
	got := string(Markdown(src))
	for _, want := range []string{
		"<h1>Handover: review</h1>", "<strong>Run:</strong>", "<code>a&lt;b&gt;</code>",
		"<em>text</em>", `<a href="https://e.com"`, "<li>one</li><li>two<ul><li>nested",
		"<ol><li>first", `<pre><code class="lang-go">fmt.Println(&#34;&lt;hi&gt;&#34;)`,
		"<th>a</th>", "<td><code>2</code></td>", "&lt;script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "javascript:") && strings.Contains(got, `href="javascript`) {
		t.Error("unsafe link rendered")
	}
}
