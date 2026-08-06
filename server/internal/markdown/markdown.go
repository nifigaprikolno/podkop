// Package markdown renders the small Markdown subset the devlog posts are
// written in.
//
// It is deliberately not a full CommonMark implementation: podkop-server ships
// as a single dependency-free binary, and a devlog needs headings, lists,
// emphasis, code, links and images — nothing more. Everything is escaped first
// and only the recognised constructs are turned back into HTML, so a post can
// never inject markup or a javascript: URL into the page.
package markdown

import (
	"fmt"
	"html"
	"strings"
)

// Render converts Markdown text to HTML.
func Render(src string) string {
	var out strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	var para []string
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		fmt.Fprintf(&out, "<p>%s</p>\n", inline(strings.Join(para, "\n")))
		para = nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			flushPara()

		case strings.HasPrefix(trimmed, "```"):
			flushPara()
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			var code []string
			for i++; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
					break
				}
				code = append(code, lines[i])
			}
			class := ""
			if lang != "" {
				class = fmt.Sprintf(" class=%q", "lang-"+html.EscapeString(lang))
			}
			fmt.Fprintf(&out, "<pre><code%s>%s</code></pre>\n", class,
				html.EscapeString(strings.Join(code, "\n")))

		case trimmed == "---" || trimmed == "***":
			flushPara()
			out.WriteString("<hr>\n")

		case strings.HasPrefix(trimmed, "#"):
			flushPara()
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			if level > 6 {
				level = 6
			}
			text := strings.TrimSpace(trimmed[level:])
			// Posts live inside a page that already owns <h1>, so shift down.
			tag := level + 1
			if tag > 6 {
				tag = 6
			}
			fmt.Fprintf(&out, "<h%d>%s</h%d>\n", tag, inline(text), tag)

		case strings.HasPrefix(trimmed, "> "):
			flushPara()
			var quote []string
			for ; i < len(lines); i++ {
				q := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(q, ">") {
					break
				}
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(q, ">")))
			}
			i--
			fmt.Fprintf(&out, "<blockquote>%s</blockquote>\n", inline(strings.Join(quote, "\n")))

		case isBullet(trimmed):
			flushPara()
			out.WriteString("<ul>\n")
			for ; i < len(lines); i++ {
				item := strings.TrimSpace(lines[i])
				if !isBullet(item) {
					break
				}
				fmt.Fprintf(&out, "<li>%s</li>\n", inline(strings.TrimSpace(item[2:])))
			}
			i--
			out.WriteString("</ul>\n")

		case isOrdered(trimmed):
			flushPara()
			out.WriteString("<ol>\n")
			for ; i < len(lines); i++ {
				item := strings.TrimSpace(lines[i])
				if !isOrdered(item) {
					break
				}
				_, rest, _ := strings.Cut(item, ".")
				fmt.Fprintf(&out, "<li>%s</li>\n", inline(strings.TrimSpace(rest)))
			}
			i--
			out.WriteString("</ol>\n")

		default:
			para = append(para, trimmed)
		}
	}
	flushPara()

	return out.String()
}

func isBullet(s string) bool {
	return len(s) > 1 && (s[0] == '-' || s[0] == '*') && s[1] == ' '
}

func isOrdered(s string) bool {
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	return digits > 0 && digits+1 < len(s) && s[digits] == '.' && s[digits+1] == ' '
}

// inline escapes the text and then applies the inline constructs. Escaping
// first is what makes the result safe: any markup in the source is already
// inert by the time we add tags of our own.
func inline(s string) string {
	s = html.EscapeString(s)
	s = images(s)
	s = links(s)
	// Code spans are pulled out before emphasis so that asterisks inside them
	// stay literal, then put back verbatim.
	s, spans := extractCode(s)
	s = emphasis(s, "**", "strong")
	s = emphasis(s, "*", "em")
	for i, span := range spans {
		s = strings.Replace(s, codePlaceholder(i), "<code>"+span+"</code>", 1)
	}
	return strings.ReplaceAll(s, "\n", "<br>\n")
}

func codePlaceholder(i int) string { return fmt.Sprintf("\x00code%d\x00", i) }

func extractCode(s string) (string, []string) {
	var spans []string
	var b strings.Builder
	for {
		start := strings.Index(s, "`")
		if start < 0 {
			break
		}
		rest := s[start+1:]
		end := strings.Index(rest, "`")
		if end < 0 {
			break
		}
		b.WriteString(s[:start])
		b.WriteString(codePlaceholder(len(spans)))
		spans = append(spans, rest[:end])
		s = rest[end+1:]
	}
	b.WriteString(s)
	return b.String(), spans
}

func emphasis(s, marker, tag string) string {
	if tag == "" {
		return s
	}
	var b strings.Builder
	open := false
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], marker) {
			// A lone marker at the end of the text stays literal.
			if !open && !strings.Contains(s[i+len(marker):], marker) {
				b.WriteString(marker)
				i += len(marker)
				continue
			}
			if open {
				fmt.Fprintf(&b, "</%s>", tag)
			} else {
				fmt.Fprintf(&b, "<%s>", tag)
			}
			open = !open
			i += len(marker)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	if open {
		fmt.Fprintf(&b, "</%s>", tag)
	}
	return b.String()
}

func images(s string) string { return linkLike(s, true) }
func links(s string) string  { return linkLike(s, false) }

// linkLike rewrites [text](url) and ![alt](url). URLs are restricted to http,
// https and site-relative paths — a javascript: or data: URL in a post would
// otherwise become a working script the moment someone clicks it.
func linkLike(s string, image bool) string {
	prefix := "["
	if image {
		prefix = "!["
	}
	var b strings.Builder
	for {
		start := strings.Index(s, prefix)
		if start < 0 {
			break
		}
		if !image && start > 0 && s[start-1] == '!' {
			// Leave image syntax to the image pass.
			b.WriteString(s[:start+1])
			s = s[start+1:]
			continue
		}
		rest := s[start+len(prefix):]
		mid := strings.Index(rest, "](")
		if mid < 0 {
			break
		}
		end := strings.Index(rest[mid:], ")")
		if end < 0 {
			break
		}
		text := rest[:mid]
		url := rest[mid+2 : mid+end]

		b.WriteString(s[:start])
		if u, ok := safeURL(url); ok {
			if image {
				fmt.Fprintf(&b, "<img src=%q alt=%q loading=\"lazy\">", u, text)
			} else {
				fmt.Fprintf(&b, "<a href=%q rel=\"noopener noreferrer\">%s</a>", u, text)
			}
		} else {
			b.WriteString(text)
		}
		s = rest[mid+end+1:]
	}
	b.WriteString(s)
	return b.String()
}

// safeURL accepts absolute http(s) URLs and site-relative paths only.
func safeURL(u string) (string, bool) {
	u = strings.TrimSpace(u)
	// The text was HTML-escaped before we got here.
	unescaped := strings.ToLower(html.UnescapeString(u))
	switch {
	case strings.HasPrefix(unescaped, "http://"), strings.HasPrefix(unescaped, "https://"):
		return u, true
	case strings.HasPrefix(u, "/"), strings.HasPrefix(u, "#"):
		return u, true
	default:
		return "", false
	}
}
