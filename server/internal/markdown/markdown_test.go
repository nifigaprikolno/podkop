package markdown

import (
	"strings"
	"testing"
)

func TestRenderBlocks(t *testing.T) {
	got := Render(strings.Join([]string{
		"# Title",
		"",
		"First paragraph.",
		"",
		"- one",
		"- two",
		"",
		"1. first",
		"2. second",
		"",
		"> quoted",
		"",
		"---",
		"",
		"```go",
		"fmt.Println(1 < 2)",
		"```",
	}, "\n"))

	for _, want := range []string{
		"<h2>Title</h2>", // headings shift down: the page owns <h1>
		"<p>First paragraph.</p>",
		"<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
		"<ol>\n<li>first</li>\n<li>second</li>\n</ol>",
		"<blockquote>quoted</blockquote>",
		"<hr>",
		`<pre><code class="lang-go">fmt.Println(1 &lt; 2)</code></pre>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestRenderInline(t *testing.T) {
	got := Render("**bold** and *italic* and `a*b` and [link](https://example.com) and ![alt](/media/x.png)")

	for _, want := range []string{
		"<strong>bold</strong>",
		"<em>italic</em>",
		"<code>a*b</code>", // asterisks inside code stay literal
		`<a href="https://example.com" rel="noopener noreferrer">link</a>`,
		`<img src="/media/x.png" alt="alt" loading="lazy">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// A post is operator-authored, but it must still be unable to inject markup or
// a script URL into the public page.
func TestRenderEscapesHTMLAndRejectsUnsafeURLs(t *testing.T) {
	got := Render("<script>alert(1)</script> [x](javascript:alert(1)) ![y](data:image/svg+xml,<svg/onload=alert(1)>)")

	if strings.Contains(got, "<script>") {
		t.Errorf("raw script tag survived: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("script tag was not escaped: %s", got)
	}
	if strings.Contains(got, "javascript:") && strings.Contains(got, "<a href") {
		t.Errorf("javascript: URL became a link: %s", got)
	}
	if strings.Contains(got, "<img") {
		t.Errorf("data: URL became an image: %s", got)
	}
}

func TestRenderLoneMarkersStayLiteral(t *testing.T) {
	got := Render("2 * 3 = 6 and `unclosed")
	if strings.Contains(got, "<em>") {
		t.Errorf("a lone asterisk became emphasis: %s", got)
	}
	if strings.Contains(got, "<code>") {
		t.Errorf("an unclosed backtick became code: %s", got)
	}
}
