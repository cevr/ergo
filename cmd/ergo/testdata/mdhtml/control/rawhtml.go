package main

import "strings"

// CommonMark's raw HTML: the seven kinds of HTML block and the inline
// tags, read by hand (no regular expressions, as in the Ergo program).

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// tagName is the end of a tag name at i (a letter, then letters, digits
// and hyphens), or i.
func tagName(s string, i int) int {
	if i >= len(s) || !asciiLetter(s[i]) {
		return i
	}
	i++
	for i < len(s) && (asciiAlnum(s[i]) || s[i] == '-') {
		i++
	}
	return i
}

func attrNameStart(c byte) bool { return asciiLetter(c) || c == '_' || c == ':' }
func attrNameByte(c byte) bool {
	return asciiAlnum(c) || c == '_' || c == ':' || c == '.' || c == '-'
}

// attrValue is the end of an attribute value at i, or -1.
func attrValue(s string, i int) int {
	if i >= len(s) {
		return -1
	}
	switch s[i] {
	case '"', '\'':
		j := strings.IndexByte(s[i+1:], s[i])
		if j < 0 {
			return -1
		}
		return i + 1 + j + 1
	}
	j := i
	for j < len(s) && !isSpace(s[j]) && strings.IndexByte("\"'=<>`", s[j]) < 0 && s[j] > ' ' {
		j++
	}
	if j == i {
		return -1
	}
	return j
}

// openTag is the length of the open tag that s starts with, or 0.
func openTag(s string) int {
	if len(s) < 2 || s[0] != '<' {
		return 0
	}
	i := tagName(s, 1)
	if i == 1 {
		return 0
	}
	for {
		j := skipSpace(s, i)
		if j == i || j >= len(s) || !attrNameStart(s[j]) {
			break
		}
		k := j + 1
		for k < len(s) && attrNameByte(s[k]) {
			k++
		}
		m := skipSpace(s, k)
		if m < len(s) && s[m] == '=' {
			v := attrValue(s, skipSpace(s, m+1))
			if v < 0 {
				return 0
			}
			i = v
		} else {
			i = k
		}
	}
	i = skipSpace(s, i)
	if i < len(s) && s[i] == '/' {
		i++
	}
	if i < len(s) && s[i] == '>' {
		return i + 1
	}
	return 0
}

// closeTag is the length of the closing tag that s starts with, or 0.
func closeTag(s string) int {
	if len(s) < 3 || s[0] != '<' || s[1] != '/' {
		return 0
	}
	i := tagName(s, 2)
	if i == 2 {
		return 0
	}
	i = skipSpace(s, i)
	if i < len(s) && s[i] == '>' {
		return i + 1
	}
	return 0
}

// until is the end of the first sep in s at or after i, or 0.
func until(s string, i int, sep string) int {
	if i > len(s) {
		return 0
	}
	j := strings.Index(s[i:], sep)
	if j < 0 {
		return 0
	}
	return i + j + len(sep)
}

// htmlTag is the length of the inline raw HTML that s starts with, or 0.
func htmlTag(s string) int {
	if len(s) < 2 || s[0] != '<' {
		return 0
	}
	switch c := s[1]; {
	case c == '/':
		return closeTag(s)
	case c == '?':
		return until(s, 2, "?>")
	case c == '!':
		if strings.HasPrefix(s, "<!-->") {
			return 5
		}
		if strings.HasPrefix(s, "<!--->") {
			return 6
		}
		if strings.HasPrefix(s, "<!--") {
			return until(s, 4, "-->")
		}
		if strings.HasPrefix(s, "<![CDATA[") {
			return until(s, 9, "]]>")
		}
		if len(s) > 2 && asciiLetter(s[2]) {
			return until(s, 3, ">")
		}
		return 0
	}
	return openTag(s)
}

var blockTags = []string{"address", "article", "aside", "base", "basefont", "blockquote", "body", "caption", "center", "col", "colgroup", "dd", "details", "dialog", "dir", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "frame", "frameset", "h1", "h2", "h3", "h4", "h5", "h6", "head", "header", "hr", "html", "iframe", "legend", "li", "link", "main", "menu", "menuitem", "nav", "noframes", "ol", "optgroup", "option", "p", "param", "search", "section", "summary", "table", "tbody", "td", "tfoot", "th", "thead", "title", "tr", "track", "ul"}

var rawTags = []string{"script", "pre", "textarea", "style"}

func oneOf(name string, names []string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// htmlBlockStarts says whether the line s (from its first non-space)
// starts an HTML block of kind t (1 to 7).
func htmlBlockStarts(t int, s string) bool {
	switch t {
	case 1:
		e := tagName(s, 1)
		if !oneOf(strings.ToLower(s[1:e]), rawTags) {
			return false
		}
		return e == len(s) || isSpace(s[e]) || s[e] == '>'
	case 2:
		return strings.HasPrefix(s, "<!--")
	case 3:
		return strings.HasPrefix(s, "<?")
	case 4:
		return len(s) > 2 && s[1] == '!' && asciiLetter(s[2])
	case 5:
		return strings.HasPrefix(s, "<![CDATA[")
	case 6:
		i := 1
		if i < len(s) && s[i] == '/' {
			i++
		}
		e := tagName(s, i)
		if !oneOf(strings.ToLower(s[i:e]), blockTags) {
			return false
		}
		return e == len(s) || isSpace(s[e]) || s[e] == '>' || strings.HasPrefix(s[e:], "/>")
	case 7:
		n := openTag(s)
		if n == 0 {
			n = closeTag(s)
		}
		return n > 0 && isBlank(s[n:])
	}
	return false
}

// htmlBlockEnds says whether the line s ends an HTML block of kind t (1
// to 5).
func htmlBlockEnds(t int, s string) bool {
	switch t {
	case 1:
		l := strings.ToLower(s)
		for _, tag := range rawTags {
			if strings.Contains(l, "</"+tag+">") {
				return true
			}
		}
		return false
	case 2:
		return strings.Contains(s, "-->")
	case 3:
		return strings.Contains(s, "?>")
	case 4:
		return strings.Contains(s, ">")
	case 5:
		return strings.Contains(s, "]]>")
	}
	return false
}
