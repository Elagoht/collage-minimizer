package minimizer

import (
	"bytes"
	"encoding/json"
	"strings"
)

// This file is the whole of the minification. Everything it does is conservative
// by construction, and that is a decision rather than a limitation.
//
// A correct minifier for HTML, CSS or JavaScript is a parser. Minifiers that are
// not parsers — the regex kind — work on almost every input and corrupt the rest
// silently: a regex literal containing "//", an apostrophe inside a CSS content
// string, a newline JavaScript needed for automatic semicolon insertion. Corrupting
// one page in a thousand, with no error anywhere, is far worse than saving less.
//
// So these are scanners, not parsers. They know where strings, comments and other
// protected regions begin and end, and outside those regions they remove only what
// cannot carry meaning. JavaScript in particular keeps a line break wherever a
// semicolon may stand for it, because removing one there can change what a program
// means; what goes is indentation, trailing space, comments, and the line breaks
// no semicolon can stand for.

// minifyJSON compacts JSON exactly, via the standard library. This is the only one
// of the four that is lossless by construction rather than by care: encoding/json
// knows the grammar completely.
func minifyJSON(src []byte) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, src); err != nil {
		// Not valid JSON. Whatever it is, it is not this function's business to
		// guess at, and returning it untouched is the only safe answer.
		return src
	}
	return out.Bytes()
}

// protectedHTML are elements whose text content is significant to the byte. Inside
// them nothing is collapsed as HTML: <pre> and <textarea> render their whitespace,
// and <script> and <style> hold other languages entirely — which minifyHTMLWith
// hands to that language's own minifier, when it is enabled.
var protectedHTML = []string{"pre", "textarea", "script", "style"}

// languages are the inline languages minifyHTMLWith minifies: a <script> of
// JavaScript under js, one of JSON under json, a <style> under css. Each follows
// the key that turns the same language on for a mounted file.
type languages struct{ js, css, json bool }

// minifyHTML minifies HTML and leaves every inline script and style verbatim.
func minifyHTML(src []byte) []byte { return minifyHTMLWith(src, languages{}) }

// minifyHTMLWith removes comments and collapses runs of whitespace, and minifies
// the inline languages langs enables.
//
// Whitespace in HTML is not free to delete: a run of it between two inline elements
// renders as a single space, and removing it joins two words. So a run is collapsed
// to one space rather than removed — except where it sits between a ">" and a "<"
// with nothing else in it, or before the first tag or after the last, which render
// as nothing and can go entirely.
//
// Conditional comments are kept. They are comments to a parser and instructions to
// the browsers that read them.
func minifyHTMLWith(src []byte, langs languages) []byte {
	out := make([]byte, 0, len(src))
	i := 0

	for i < len(src) {
		// A protected element keeps its tags as written, and its content unless
		// that content is a language langs enables.
		if src[i] == '<' {
			if name, end, ok := openingTagName(src, i); ok && contains(protectedHTML, name) {
				closing := "</" + name
				stop := indexFoldFrom(src, closing, end)
				if stop < 0 {
					out = append(out, src[i:]...)
					return out
				}
				tagEnd := bytes.IndexByte(src[stop:], '>')
				if tagEnd < 0 {
					out = append(out, src[i:]...)
					return out
				}
				out = append(out, src[i:end]...)
				out = append(out, minifyInline(name, src[i:end], src[end:stop], langs)...)
				out = append(out, src[stop:stop+tagEnd+1]...)
				i = stop + tagEnd + 1
				continue
			}
		}

		// Comments, except the conditional kind.
		if bytes.HasPrefix(src[i:], []byte("<!--")) {
			end := bytes.Index(src[i:], []byte("-->"))
			if end < 0 {
				out = append(out, src[i:]...)
				return out
			}
			comment := src[i : i+end+3]
			if bytes.HasPrefix(comment, []byte("<!--[if")) || bytes.Contains(comment, []byte("<![endif]")) {
				out = append(out, comment...)
			}
			i += end + 3
			continue
		}

		if isSpace(src[i]) {
			j := i
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			// Between tags, before the first or after the last, the run renders as
			// nothing; anywhere else it renders as one space, and deleting it would
			// join two words.
			between := len(out) > 0 && out[len(out)-1] == '>' && j < len(src) && src[j] == '<'
			leading := len(out) == 0 && j < len(src) && src[j] == '<'
			trailing := j == len(src) && len(out) > 0 && out[len(out)-1] == '>'
			if !between && !leading && !trailing {
				out = append(out, ' ')
			}
			i = j
			continue
		}

		out = append(out, src[i])
		i++
	}
	return out
}

// minifyInline minifies the content of a <script> or <style> whose language langs
// enables, and returns anything else as it was: <pre>, <textarea>, a type it does
// not know, a result no smaller than the input.
//
// The whitespace at either end of the content goes too. Before the first statement
// or rule it carries nothing, and after the last the end of the element ends the
// statement as a newline would.
func minifyInline(name string, tag, body []byte, langs languages) []byte {
	var out []byte
	switch {
	case name == "script" && langs.js && scriptLanguage(tag) == "js":
		out = bytes.TrimSpace(minifyJS(body))
	case name == "script" && langs.json && scriptLanguage(tag) == "json":
		out = minifyJSON(body)
	case name == "style" && langs.css && styleIsCSS(tag):
		out = bytes.TrimSpace(minifyCSS(body))
	default:
		return body
	}
	if len(out) >= len(body) {
		return body
	}
	return out
}

// scriptLanguage names what a <script> holds, from its type attribute: "js" for
// a classic script or a module, "json" for data — structured data, an import map,
// speculation rules — and "" for a type this does not know, which stays verbatim.
func scriptLanguage(tag []byte) string {
	value, _ := attrValue(tag, "type")
	media, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(value)), ";")
	media = strings.TrimSpace(media)
	switch {
	case media == "", media == "module", media == "text/javascript", media == "application/javascript",
		media == "text/ecmascript", media == "application/ecmascript":
		return "js"
	case media == "importmap", media == "speculationrules",
		strings.HasSuffix(media, "/json"), strings.HasSuffix(media, "+json"):
		return "json"
	default:
		return ""
	}
}

// styleIsCSS reports whether a <style> holds CSS: it names no type, or text/css.
func styleIsCSS(tag []byte) bool {
	value, _ := attrValue(tag, "type")
	media, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(value)), ";")
	media = strings.TrimSpace(media)
	return media == "" || media == "text/css"
}

// attrValue returns the value of the attribute named name, ASCII case ignored, in
// the opening tag tag — "<script type=module>" through its ">".
func attrValue(tag []byte, name string) (string, bool) {
	j := 1
	for j < len(tag) && (isLetter(tag[j]) || isDigit(tag[j])) {
		j++
	}
	for j < len(tag) {
		for j < len(tag) && (isSpace(tag[j]) || tag[j] == '/') {
			j++
		}
		start := j
		for j < len(tag) && !isSpace(tag[j]) && tag[j] != '=' && tag[j] != '>' && tag[j] != '/' {
			j++
		}
		if start == j {
			return "", false
		}
		attr := strings.ToLower(string(tag[start:j]))
		for j < len(tag) && isSpace(tag[j]) {
			j++
		}
		value := ""
		if j < len(tag) && tag[j] == '=' {
			j++
			for j < len(tag) && isSpace(tag[j]) {
				j++
			}
			if j < len(tag) && (tag[j] == '"' || tag[j] == '\'') {
				quote := tag[j]
				end := bytes.IndexByte(tag[j+1:], quote)
				if end < 0 {
					return "", false
				}
				value = string(tag[j+1 : j+1+end])
				j += end + 2
			} else {
				vstart := j
				for j < len(tag) && !isSpace(tag[j]) && tag[j] != '>' {
					j++
				}
				value = string(tag[vstart:j])
			}
		}
		if attr == name {
			return value, true
		}
	}
	return "", false
}

// minifyCSS removes comments and collapses whitespace, leaving strings alone.
//
// Whitespace inside a CSS string is content — content: "a  b" is two spaces — and
// whitespace between two values is a separator that cannot be dropped, so runs
// collapse to a single space rather than vanishing.
//
// The spaces around punctuation stay too, which is where most of the remaining
// saving would be. "a :hover" and "a:hover" are a descendant selector and a
// pseudo-class, and telling a selector's colon from a declaration's requires
// knowing which side of the brace you are on — which requires parsing. The saving
// is not worth changing which elements a stylesheet applies to.
func minifyCSS(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0

	for i < len(src) {
		switch {
		case src[i] == '"' || src[i] == '\'':
			end := scanCSSString(src, i)
			out = append(out, src[i:end]...)
			i = end

		case bytes.HasPrefix(src[i:], []byte("/*")):
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			i += 2 + end + 2
			// A comment can sit between two whitespace runs. Removing it leaves
			// them adjacent, and neither run's own pass will see the other.
			for i < len(src) && isSpace(src[i]) && len(out) > 0 && out[len(out)-1] == ' ' {
				i++
			}

		case isSpace(src[i]):
			j := i
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			if len(out) > 0 && j < len(src) {
				out = append(out, ' ')
			}
			i = j

		default:
			out = append(out, src[i])
			i++
		}
	}
	return out
}

// minifyJS removes comments and whitespace, and keeps a line break wherever a
// semicolon may stand for it.
//
// JavaScript inserts semicolons at line breaks. "return\n  x" is "return; x", and
// joining those two lines changes what the program does. Deciding exactly where one
// is inserted requires knowing where every statement ends, which requires a parser —
// so this does not try. It joins a line only after a token that cannot end a
// statement: an opening bracket, a comma, a semicolon, a colon, or an operator
// that needs something after it. There no semicolon can be inserted, and the break
// is only whitespace. After anything else — a name, a closing bracket, a string, a
// regular expression, "++" — the break stays.
//
// It tracks strings, template literals and regular expression literals, because
// each of them can contain the character sequences that start a comment. A minifier
// that treats "https://example.com" inside a string as the start of a line comment
// deletes the rest of the line.
func minifyJS(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0
	// broken is set by a line break since the last token, decided when the next
	// one arrives: a comment in between says nothing either way.
	broken := false
	// afterRegex is set while the last token is a regular expression literal,
	// whose closing "/" ends an expression as a name does.
	afterRegex := false

	emit := func(next int) {
		if broken {
			broken = false
			switch {
			case len(out) == 0:
			case !joinable(out, afterRegex) || bytes.HasPrefix(src[next:], []byte("-->")):
				out = append(out, '\n')
			case fuses(out[len(out)-1], src[next]):
				out = append(out, ' ')
			}
		}
		afterRegex = false
	}

	for i < len(src) {
		switch {
		case src[i] == '"' || src[i] == '\'' || src[i] == '`':
			emit(i)
			end := scanJSString(src, i)
			out = append(out, src[i:end]...)
			i = end

		case bytes.HasPrefix(src[i:], []byte("//")):
			end := bytes.IndexByte(src[i:], '\n')
			if end < 0 {
				return out
			}
			i += end // leave the newline: it may be terminating a statement

		case bytes.HasPrefix(src[i:], []byte("/*")):
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			// A block comment spanning lines stood where a line break stood, and
			// the break may have been terminating a statement.
			if bytes.Contains(src[i:i+2+end+2], []byte("\n")) {
				broken = true
			}
			i += 2 + end + 2

		case src[i] == '/' && regexCanStartHere(out):
			emit(i)
			end := scanJSRegex(src, i)
			out = append(out, src[i:end]...)
			i = end
			// Conservative when the scan found a division after all: a kept break.
			afterRegex = true

		case src[i] == '\n':
			broken = true
			i++

		case src[i] == ' ' || src[i] == '\t' || src[i] == '\r':
			j := i
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r') {
				j++
			}
			// A run at the start of a line, or before one, carries nothing.
			trailing := j < len(src) && src[j] == '\n'
			leading := len(out) == 0 || broken
			if !trailing && !leading && j < len(src) {
				out = append(out, ' ')
			}
			i = j

		default:
			emit(i)
			out = append(out, src[i])
			i++
		}
	}
	return out
}

// joinable reports whether a line break after out can go: whether out ends in a
// token no statement can end with, so no semicolon can be inserted at the break.
//
// "/" is left out as a division, since a regular expression the scanner took for
// one would end the line. "++" and "--" end an expression. A "." after a digit
// may be the end of a number, "1.".
func joinable(out []byte, afterRegex bool) bool {
	if afterRegex {
		return false
	}
	last := out[len(out)-1]
	before := byte(0)
	if len(out) > 1 {
		before = out[len(out)-2]
	}
	switch last {
	case '{', '(', '[', ',', ';', ':', '=', '*', '%', '&', '|', '^', '!', '~', '<', '>', '?':
		return true
	case '+', '-':
		return before != last
	case '.':
		return !isDigit(before)
	default:
		return false
	}
}

// fuses reports whether a and b, written next to each other, could read as one
// token they were not: "+" and "+" as "++", "<" and "!" as the start of "<!--".
func fuses(a, b byte) bool {
	return strings.IndexByte("=+-*/%&|^!~<>?.", a) >= 0 && strings.IndexByte("=+-*/%&|^!~<>?.", b) >= 0
}

// scanCSSString returns the index just past the string literal starting at i.
func scanCSSString(src []byte, i int) int {
	quote := src[i]
	j := i + 1
	for j < len(src) {
		if src[j] == '\\' {
			j += 2
			continue
		}
		if src[j] == quote {
			return j + 1
		}
		j++
	}
	return len(src)
}

// scanJSString returns the index just past the string or template literal starting
// at i. A template's ${…} holds JavaScript, where braces and strings — another
// template among them — may contain a "}" that does not end it, so the expression
// is scanned as code rather than counted.
func scanJSString(src []byte, i int) int {
	quote := src[i]
	j := i + 1
	for j < len(src) {
		switch {
		case src[j] == '\\':
			j += 2
			continue
		case quote == '`' && src[j] == '$' && j+1 < len(src) && src[j+1] == '{':
			j = scanTemplateExpression(src, j+2)
			continue
		case src[j] == quote:
			return j + 1
		}
		j++
	}
	return len(src)
}

// scanTemplateExpression returns the index just past the "}" that closes the
// template expression whose code starts at i.
func scanTemplateExpression(src []byte, i int) int {
	depth := 0
	j := i
	for j < len(src) {
		switch src[j] {
		case '"', '\'', '`':
			j = scanJSString(src, j)
			continue
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return j + 1
			}
			depth--
		}
		j++
	}
	return len(src)
}

// scanJSRegex returns the index just past the regular expression literal starting
// at i, including its flags. A character class may hold an unescaped "/", so the
// class is tracked.
func scanJSRegex(src []byte, i int) int {
	j := i + 1
	inClass := false
	for j < len(src) {
		switch {
		case src[j] == '\\':
			j += 2
			continue
		case src[j] == '[':
			inClass = true
		case src[j] == ']':
			inClass = false
		case src[j] == '/' && !inClass:
			j++
			for j < len(src) && isLetter(src[j]) {
				j++
			}
			return j
		case src[j] == '\n':
			// A regex literal cannot span lines, so this was a division after all.
			return i + 1
		}
		j++
	}
	return len(src)
}

// regexCanStartHere decides whether a "/" begins a regular expression literal or is
// a division operator, from the last significant token emitted.
//
// This is the classic ambiguity in JavaScript's grammar and it cannot be resolved
// perfectly without parsing. The heuristic is the usual one: a "/" after a value —
// an identifier, a number, a closing bracket — divides; after a keyword that takes
// an expression, "return /re/", or anywhere else, it opens a regex. It errs toward
// treating "/" as division, which leaves a regex unscanned and its contents merely
// copied through unchanged rather than mangled.
func regexCanStartHere(out []byte) bool {
	i := len(out) - 1
	for i >= 0 && isSpace(out[i]) {
		i--
	}
	if i < 0 {
		return true
	}
	c := out[i]
	if c == ')' || c == ']' || c == '}' {
		return false
	}
	if !isWordByte(c) {
		return true
	}
	end := i + 1
	for i >= 0 && isWordByte(out[i]) {
		i--
	}
	// A property is a value whatever it is called: a.return / 2.
	if i >= 0 && out[i] == '.' {
		return false
	}
	return contains(expressionKeywords, string(out[i+1:end]))
}

// expressionKeywords are the keywords after which an expression, and so a regular
// expression literal, may begin.
var expressionKeywords = []string{
	"return", "typeof", "instanceof", "in", "of", "new", "delete", "void",
	"throw", "case", "do", "else", "yield", "await",
}

// isWordByte reports whether c can be part of an identifier: a non-ASCII byte is
// taken to be one, since a name may be written in any script.
func isWordByte(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '$' || c >= 0x80
}

func isSpace(c byte) bool  { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// openingTagName returns the lowercased element name of the tag starting at i and
// the index just past its ">". A ">" inside a quoted attribute value does not end
// the tag: taken for the end, it would put the element's content in the middle of
// the attribute.
func openingTagName(src []byte, i int) (string, int, bool) {
	j := i + 1
	if j >= len(src) || !isLetter(src[j]) {
		return "", 0, false
	}
	start := j
	for j < len(src) && (isLetter(src[j]) || isDigit(src[j])) {
		j++
	}
	name := strings.ToLower(string(src[start:j]))
	for j < len(src) {
		switch src[j] {
		case '>':
			return name, j + 1, true
		case '=':
			j++
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == '"' || src[j] == '\'') {
				end := bytes.IndexByte(src[j+1:], src[j])
				if end < 0 {
					return "", 0, false
				}
				j += end + 2
			}
			continue
		}
		j++
	}
	return "", 0, false
}

// indexFoldFrom finds needle in src at or after from, ignoring ASCII case.
//
// Only ASCII is lowercased. bytes.ToLower does not keep lengths — Turkish İ is two
// bytes and lowercases to i, one — and an index into its result is not an index
// into src.
func indexFoldFrom(src []byte, needle string, from int) int {
	lower := make([]byte, len(src)-from)
	for i, c := range src[from:] {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower[i] = c
	}
	idx := bytes.Index(lower, []byte(strings.ToLower(needle)))
	if idx < 0 {
		return -1
	}
	return from + idx
}
