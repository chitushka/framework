// Package doccheck validates repository Markdown links and source-backed fences.
// It intentionally performs no network requests and supports the repository's
// inline/reference Markdown links, ATX headings and backtick/tilde fenced blocks.
package doccheck

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Report struct{ Documents, Links, Snippets int }
type link struct {
	destination string
	line        int
}
type document struct {
	path    string
	anchors map[string]bool
	links   []link
	sources map[string]bool
}

var definition = regexp.MustCompile(`^\s{0,3}\[([^]]+)\]:\s*(\S.*)$`)
var reference = regexp.MustCompile(`!?\[([^]\n]+)\]\[([^]\n]*)\]`)
var inlineCode = regexp.MustCompile("`+[^`]*`+")
var htmlID = regexp.MustCompile(`<(?:a|span)\s+[^>]*(?:id|name)=["']([^"']+)["'][^>]*>`)

func Check(root string) (Report, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Report{}, err
	}
	docs := map[string]*document{}
	var problems []error
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", ".idea", ".vscode", "node_modules", "public", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("markdown symlink is not supported: %s", path)
			}
			doc, err := parse(root, path)
			if err != nil {
				problems = append(problems, err)
			} else {
				docs[path] = doc
			}
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	if len(docs) == 0 {
		problems = append(problems, errors.New("no Markdown documents found"))
	}
	report := Report{Documents: len(docs)}
	sources := map[string]bool{}
	paths := make([]string, 0, len(docs))
	for path := range docs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		doc := docs[path]
		for source := range doc.sources {
			sources[source] = true
			report.Snippets++
		}
		for _, item := range doc.links {
			report.Links++
			if err := checkLink(root, doc, item, docs); err != nil {
				problems = append(problems, fmt.Errorf("%s:%d: %w", relative(root, path), item.line, err))
			}
		}
	}
	// Every executable guide example must appear as a source-backed fence. This
	// prevents adding an unverified copy while the CI tests a different main.go.
	examples := filepath.Join(root, "examples", "guide")
	if _, err := os.Stat(examples); err == nil {
		err := filepath.WalkDir(examples, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && entry.Name() == "main.go" && !sources[path] {
				problems = append(problems, fmt.Errorf("%s: no source-backed documentation fence", relative(root, path)))
			}
			return nil
		})
		if err != nil {
			problems = append(problems, err)
		}
	}
	return report, errors.Join(problems...)
}

func parse(root, path string) (*document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	doc := &document{path: path, anchors: map[string]bool{}, sources: map[string]bool{}}
	definitions := map[string]string{}
	var prose []link
	duplicates := map[string]int{}
	var fence, source string
	var content []string
	start := 0
	for n, line := range lines {
		trim := strings.TrimSpace(line)
		if fence != "" {
			if len(trim) >= len(fence) && strings.Trim(trim, string(fence[0])) == "" {
				if source != "" {
					target, err := localPath(root, filepath.Dir(path), source)
					if err != nil {
						return nil, fmt.Errorf("%s:%d: %w", relative(root, path), start, err)
					}
					expected, err := os.ReadFile(target)
					if err != nil {
						return nil, err
					}
					actual := strings.Join(content, "\n") + "\n"
					if !bytes.Equal([]byte(actual), bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n"))) {
						return nil, fmt.Errorf("%s:%d: source fence differs from %s", relative(root, path), start, source)
					}
					doc.sources[target] = true
				}
				fence, source = "", ""
				content = nil
			} else {
				content = append(content, line)
			}
			continue
		}
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			count := 0
			for count < len(trim) && trim[count] == trim[0] {
				count++
			}
			fence = trim[:count]
			start = n + 1
			for _, field := range strings.Fields(trim[count:]) {
				if strings.HasPrefix(field, "file=") {
					source = strings.TrimPrefix(field, "file=")
					if source == "" {
						return nil, fmt.Errorf("%s:%d: empty source path", path, n+1)
					}
				}
			}
			continue
		}
		if match := definition.FindStringSubmatch(line); match != nil {
			definitions[strings.ToLower(match[1])] = destination(match[2])
			continue
		}
		for _, match := range htmlID.FindAllStringSubmatch(line, -1) {
			doc.anchors[match[1]] = true
		}
		if strings.HasPrefix(trim, "#") {
			label := strings.TrimLeft(trim, "#")
			if strings.HasPrefix(label, " ") {
				slug := heading(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(label), "#")))
				id := slug
				if count := duplicates[slug]; count > 0 {
					id = fmt.Sprintf("%s-%d", slug, count)
				}
				duplicates[slug]++
				doc.anchors[id] = true
			}
		}
		prose = append(prose, link{destination: inlineCode.ReplaceAllString(line, ""), line: n + 1})
	}
	if fence != "" {
		return nil, fmt.Errorf("%s:%d: unclosed code fence", relative(root, path), start)
	}
	for _, line := range prose {
		text := line.destination
		for _, match := range reference.FindAllStringSubmatch(text, -1) {
			id := match[2]
			if id == "" {
				id = match[1]
			}
			target, ok := definitions[strings.ToLower(id)]
			if !ok {
				return nil, fmt.Errorf("%s:%d: undefined reference %s", path, line.line, id)
			}
			doc.links = append(doc.links, link{target, line.line})
		}
		for pos := 0; pos < len(text); {
			index := strings.Index(text[pos:], "](")
			if index < 0 {
				break
			}
			index += pos
			if !strings.Contains(text[:index], "[") {
				pos = index + 2
				continue
			}
			end, depth := index+2, 1
			for end < len(text) && depth > 0 {
				if text[end] == '\\' {
					end += 2
					continue
				}
				switch text[end] {
				case '(':
					depth++
				case ')':
					depth--
				}
				end++
			}
			if depth != 0 {
				return nil, fmt.Errorf("%s:%d: unclosed link destination", path, line.line)
			}
			doc.links = append(doc.links, link{destination(text[index+2 : end-1]), line.line})
			pos = end
		}
	}
	return doc, nil
}

func destination(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<") {
		if end := strings.IndexByte(value, '>'); end >= 0 {
			return value[1:end]
		}
	}
	if end := strings.Index(value, ` "`); end >= 0 {
		value = value[:end]
	}
	return strings.ReplaceAll(strings.ReplaceAll(value, `\(`, "("), `\)`, ")")
}

func heading(text string) string {
	// Markdown labels contribute their visible text to heading anchors.
	text = regexp.MustCompile(`\[([^]]+)\]\([^)]+\)`).ReplaceAllString(text, "$1")
	var out strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '-' || r == '_' {
			out.WriteRune(r)
		} else if r == ' ' {
			out.WriteByte('-')
		}
	}
	return out.String()
}

func checkLink(root string, doc *document, item link, docs map[string]*document) error {
	u, err := url.Parse(item.destination)
	if err != nil {
		return err
	}
	if u.Scheme != "" {
		switch u.Scheme {
		case "http", "https":
			if u.Host != "" {
				return nil
			}
		case "mailto":
			if u.Opaque != "" {
				return nil
			}
		}
		return fmt.Errorf("invalid or unsupported external URL %q", item.destination)
	}
	if u.Host != "" {
		return fmt.Errorf("scheme-relative link is unsupported: %q", item.destination)
	}
	target := doc.path
	if u.Path != "" {
		target, err = localPath(root, filepath.Dir(doc.path), u.Path)
		if err != nil {
			return err
		}
	}
	if u.Fragment != "" && strings.EqualFold(filepath.Ext(target), ".md") {
		other, ok := docs[target]
		if !ok {
			return fmt.Errorf("target Markdown was not checked: %s", item.destination)
		}
		if !other.anchors[u.Fragment] {
			return fmt.Errorf("missing heading anchor: %s", item.destination)
		}
	}
	return nil
}

func localPath(root, base, raw string) (string, error) {
	if strings.ContainsAny(raw, "\\\x00") {
		return "", fmt.Errorf("invalid repository path %q", raw)
	}
	if strings.HasPrefix(raw, "/") {
		base = root
		raw = strings.TrimPrefix(raw, "/")
	}
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(raw)))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository: %s", raw)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("missing target %s: %w", raw, err)
	}
	resolvedRel, err := filepath.Rel(root, resolved)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("symlink escapes repository: %s", raw)
	}
	// Enforce Linux-compatible case even when running on a Windows filesystem.
	current := root
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			entries, err := os.ReadDir(current)
			if err != nil {
				return "", err
			}
			found := false
			for _, entry := range entries {
				if entry.Name() == part {
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("target case mismatch: %s", raw)
			}
			current = filepath.Join(current, part)
		}
	}
	return target, nil
}

func relative(root, path string) string {
	value, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(value)
}
