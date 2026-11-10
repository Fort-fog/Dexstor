package tools

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ValidateHTML checks edited HTML using only the Go standard library.
// This checks document structure and local assets, not full HTML5 conformance.
func (r *Router) ValidateHTML(path string) Result {
	if !strings.EqualFold(filepath.Ext(path), ".html") {
		return fail("validate_html", path, "expected .html file")
	}
	abs, err := r.safePath(path)
	if err != nil {
		return fail("validate_html", path, err.Error())
	}
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return fail("validate_html", path, "HTML file missing or not regular")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fail("validate_html", path, err.Error())
	}
	if err := checkHTMLStructure(string(data)); err != nil {
		return fail("validate_html", path, "HTML structure: "+err.Error())
	}
	if err := r.validateLocalImages(path, abs); err != nil {
		return fail("validate_html", path, err.Error())
	}
	return Result{Tool: "validate_html", Path: path, Output: "PASS: Go HTML structure and local image checks: " + path, OK: true, Exit: 0}
}

var htmlVoid = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}
var htmlOptionalClose = map[string]bool{"li": true, "p": true, "dt": true, "dd": true, "option": true, "optgroup": true, "tr": true, "td": true, "th": true, "thead": true, "tbody": true, "tfoot": true}

// checkHTMLStructure is a conservative dependency-free structural check.
// It intentionally is not presented as a complete HTML5 validator.
func checkHTMLStructure(src string) error {
	lower := strings.ToLower(src)
	if !strings.Contains(lower, "<html") || !strings.Contains(lower, "</html") {
		return fmt.Errorf("missing html root element")
	}
	var stack []string
	for i := 0; i < len(src); {
		start := strings.IndexByte(src[i:], '<')
		if start < 0 {
			break
		}
		i += start
		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				return fmt.Errorf("unclosed HTML comment")
			}
			i += 4 + end + 3
			continue
		}
		if i+1 >= len(src) {
			return fmt.Errorf("unterminated <")
		}
		if src[i+1] == '!' || src[i+1] == '?' {
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				return fmt.Errorf("unterminated declaration")
			}
			i += end + 1
			continue
		}
		closing := src[i+1] == '/'
		pos := i + 1
		if closing {
			pos++
		}
		if pos >= len(src) || !asciiLetter(src[pos]) {
			i++
			continue
		}
		begin := pos
		for pos < len(src) && (asciiLetter(src[pos]) || src[pos] >= '0' && src[pos] <= '9' || src[pos] == '-' || src[pos] == ':') {
			pos++
		}
		tag := strings.ToLower(src[begin:pos])
		quote := byte(0)
		end := pos
		for ; end < len(src); end++ {
			c := src[end]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				continue
			}
			if c == '>' {
				break
			}
		}
		if end >= len(src) {
			return fmt.Errorf("unclosed <%s> tag", tag)
		}
		selfClosing := end > pos && src[end-1] == '/'
		i = end + 1
		if closing {
			if htmlVoid[tag] {
				return fmt.Errorf("void element </%s> cannot have closing tag", tag)
			}
			if len(stack) == 0 {
				return fmt.Errorf("unexpected closing </%s>", tag)
			}
			found := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k] == tag {
					found = k
					break
				}
				if !htmlOptionalClose[stack[k]] {
					break
				}
			}
			if found < 0 {
				return fmt.Errorf("unexpected closing </%s>; open <%s>", tag, stack[len(stack)-1])
			}
			stack = stack[:found]
			continue
		}
		if !htmlVoid[tag] && !selfClosing {
			// HTML5 closes some elements implicitly when a new sibling begins.
			if len(stack) > 0 && htmlOptionalClose[tag] && stack[len(stack)-1] == tag {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, tag)
			if tag == "script" || tag == "style" {
				closeTag := "</" + tag
				offset := strings.Index(strings.ToLower(src[i:]), closeTag)
				if offset < 0 {
					return fmt.Errorf("missing closing </%s>", tag)
				}
				i += offset
			}
		}
	}
	for len(stack) > 0 && htmlOptionalClose[stack[len(stack)-1]] {
		stack = stack[:len(stack)-1]
	}
	if len(stack) > 0 {
		return fmt.Errorf("unclosed <%s>", stack[len(stack)-1])
	}
	return nil
}
func asciiLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

var quotedImageSources = regexp.MustCompile(`(?is)<img\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)

func (r *Router) validateLocalImages(htmlPath, abs string) error {
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	for _, match := range quotedImageSources.FindAllStringSubmatch(string(data), -1) {
		source := strings.TrimSpace(match[1])
		if source == "" {
			return fmt.Errorf("HTML image has empty src in %s", htmlPath)
		}
		if strings.HasPrefix(source, "//") || strings.HasPrefix(source, "data:") {
			continue
		}
		u, err := url.Parse(source)
		if err != nil {
			return fmt.Errorf("invalid img src %q: %w", source, err)
		}
		if u.IsAbs() {
			continue
		}
		if strings.HasPrefix(u.Path, "/") {
			return fmt.Errorf("HTML image %q uses a root-absolute path; use a project-local relative asset", source)
		}
		local := filepath.Clean(filepath.Join(filepath.Dir(htmlPath), filepath.FromSlash(u.Path)))
		target, err := r.safePath(local)
		if err != nil {
			return fmt.Errorf("HTML image %q escapes the project: %w", source, err)
		}
		info, err := os.Stat(target)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("HTML image %q missing in project; copy it into assets and update src", source)
		}
	}
	return nil
}
