package main

// The scan command lists every Swift package's tests from source, the way
// `swift test list` names them, without resolving dependencies or compiling:
//
//   - test targets come from `.testTarget(name:path:)` in Package.swift, read
//     as text, with SwiftPM's default path Tests/<name>;
//   - XCTest: `func test…()` instance methods without parameters in classes
//     that inherit from XCTestCase, as `Target.Class/testMethod`;
//   - Swift Testing: `@Test` functions, as `Target.f(label:)` or
//     `Target.Suite/f(label:)`, with nested types joined by "/".
//
// Conditional compilation blocks are evaluated with the given compilation
// conditions (-D), os(Linux), and the engine's architecture. The scan cannot
// see macros, generated code or tests a manifest computes, so the shared run
// reports any test it lists but SwiftPM does not run as "did not run".

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type packageScan struct {
	Tests []string `json:"tests"`
	// Error names the file discovery could not read. Only that package fails.
	Error string `json:"error"`
}

var skippedDirs = map[string]bool{
	".build": true, ".git": true, "node_modules": true, "DerivedData": true, "xcuserdata": true,
}

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func runScan(args []string) error {
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	root := flags.String("root", ".", "workspace directory")
	output := flags.String("output", "", "result directory")
	var defines stringList
	flags.Var(&defines, "define", "active compilation condition (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	conditions := map[string]bool{}
	for _, d := range defines {
		conditions[d] = true
	}
	roots, err := packageRoots(*root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*output, "packages"), 0o755); err != nil {
		return err
	}
	// Ranks in byte order, as the module computes them: identifiers never
	// become file paths.
	for i, rel := range roots {
		result := scanPackage(*root, rel, conditions)
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*output, "packages", fmt.Sprint(i)+".json"), data, 0o644); err != nil {
			return err
		}
	}
	data, err := json.Marshal(roots)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*output, "roots.json"), data, 0o644)
}

// packageRoots lists workspace-relative directories holding Package.swift.
func packageRoots(root string) ([]string, error) {
	roots := []string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && skippedDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "Package.swift" {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			roots = append(roots, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(roots)
	return roots, err
}

type testTarget struct{ name, path string }

var (
	targetCall = regexp.MustCompile(`\btestTarget\s*\(`)
	nameArg    = regexp.MustCompile(`\bname\s*:\s*"([^"\\]*)"`)
	pathArg    = regexp.MustCompile(`\bpath\s*:\s*"([^"\\]*)"`)
)

// testTargets reads `.testTarget(...)` calls from a manifest's text.
func testTargets(manifest string) []testTarget {
	var targets []testTarget
	for _, loc := range targetCall.FindAllStringIndex(manifest, -1) {
		args := balanced(manifest, loc[1]-1)
		name := nameArg.FindStringSubmatch(args)
		if name == nil {
			continue
		}
		target := testTarget{name: name[1], path: "Tests/" + name[1]}
		if p := pathArg.FindStringSubmatch(args); p != nil {
			target.path = p[1]
		}
		targets = append(targets, target)
	}
	return targets
}

// balanced returns the text inside the parentheses opening at open, skipping
// string literals.
func balanced(s string, open int) string {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i]
			}
		}
	}
	return s[open+1:]
}

// c99name is SwiftPM's module name for a target.
func c99name(name string) string {
	var b strings.Builder
	for i, r := range name {
		ok := r == '_' || r >= 0x80 || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
		} else if i == 0 && r >= '0' && r <= '9' {
			b.WriteRune('_')
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func scanPackage(root, rel string, conditions map[string]bool) packageScan {
	result := packageScan{Tests: []string{}}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	manifest, err := os.ReadFile(filepath.Join(dir, "Package.swift"))
	if err != nil {
		result.Error = path.Join(rel, "Package.swift") + ": " + err.Error()
		return result
	}
	scanner := newSwiftScanner(conditions)
	for _, target := range testTargets(string(manifest)) {
		files, err := swiftFiles(filepath.Join(dir, filepath.FromSlash(target.path)))
		if err != nil {
			result.Error = path.Join(rel, target.path) + ": " + err.Error()
			return result
		}
		for _, file := range files {
			src, err := os.ReadFile(file)
			if err != nil {
				name, _ := filepath.Rel(root, file)
				result.Error = filepath.ToSlash(name) + ": " + err.Error()
				return result
			}
			scanner.scan(c99name(target.name), src)
		}
	}
	result.Tests = scanner.identifiers()
	return result
}

// swiftFiles lists a target's sources, leaving out nested packages. A target
// directory that does not exist has no tests; swift test reports the error.
func swiftFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() && p != dir {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "Package.swift")); err == nil {
				return filepath.SkipDir
			}
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".swift") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

type token struct {
	kind byte // 'i' identifier, 'p' punctuation, 's' string, 'n' number
	text string
}

type xctestCandidate struct{ module, class, path, method string }

type swiftScanner struct {
	conditions map[string]bool
	// Superclass by class name, across the package's test targets.
	superclass map[string]string
	candidates []xctestCandidate
	ids        map[string]bool
}

func newSwiftScanner(conditions map[string]bool) *swiftScanner {
	return &swiftScanner{conditions: conditions, superclass: map[string]string{}, ids: map[string]bool{}}
}

func (s *swiftScanner) identifiers() []string {
	for _, c := range s.candidates {
		if s.isTestCase(c.class, 0) {
			s.ids[c.module+"."+c.path+"/"+c.method] = true
		}
	}
	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// isTestCase follows the package's own classes up to XCTestCase. A base class
// from a dependency counts when its name ends in TestCase.
func (s *swiftScanner) isTestCase(class string, depth int) bool {
	if class == "XCTestCase" {
		return true
	}
	super, ok := s.superclass[class]
	if !ok || depth > 32 {
		return false
	}
	if _, known := s.superclass[super]; !known && super != "XCTestCase" {
		return strings.HasSuffix(super, "TestCase")
	}
	return s.isTestCase(super, depth+1)
}

var declarationKeywords = map[string]bool{
	"func": true, "var": true, "let": true, "subscript": true, "init": true, "deinit": true,
	"override": true, "final": true, "private": true, "public": true, "internal": true,
	"fileprivate": true, "open": true, "static": true, "mutating": true, "nonmutating": true,
	"convenience": true, "required": true, "nonisolated": true, "lazy": true, "weak": true,
	"unowned": true, "dynamic": true, "class": true, "typealias": true, "async": true,
}

type scope struct {
	block bool     // a function body, closure or other non-type braces
	names []string // type path segments this scope adds
}

func (s *swiftScanner) scan(module string, src []byte) {
	tokens := lex(src, s.conditions)
	var stack []scope
	var pending *scope
	var attrs []string
	static, private := false, false
	inBlock := func() bool {
		for _, sc := range stack {
			if sc.block {
				return true
			}
		}
		return false
	}
	typePath := func() []string {
		var p []string
		for _, sc := range stack {
			p = append(p, sc.names...)
		}
		return p
	}
	reset := func() { attrs, static, private = nil, false, false }
	next := func(i int) token {
		if i+1 < len(tokens) {
			return tokens[i+1]
		}
		return token{}
	}
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t.kind == 'p' && t.text == "{":
			if pending != nil {
				stack = append(stack, *pending)
				pending = nil
			} else {
				stack = append(stack, scope{block: true})
			}
			reset()
		case t.kind == 'p' && t.text == "}":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			reset()
		case t.kind == 'p' && t.text == "@" && next(i).kind == 'i':
			attrs = append(attrs, next(i).text)
			i++
			if next(i).text == "(" {
				i = skipBalanced(tokens, i+1, "(", ")")
			}
		case t.kind == 'i' && (t.text == "static" || t.text == "class" && declarationKeywords[next(i).text]):
			static = true
		case t.kind == 'i' && (t.text == "private" || t.text == "fileprivate"):
			private = true
		case t.kind == 'i' && pending == nil && !inBlock() && (t.text == "class" || t.text == "struct" || t.text == "enum" || t.text == "actor" || t.text == "extension" || t.text == "protocol"):
			// A declaration: its name, then for a class its superclass.
			j := i + 1
			var names []string
			for j < len(tokens) && tokens[j].kind == 'i' {
				names = append(names, tokens[j].text)
				if j+2 < len(tokens) && tokens[j+1].text == "." && tokens[j+2].kind == 'i' {
					j += 2
					continue
				}
				j++
				break
			}
			if len(names) == 0 {
				continue
			}
			if t.text == "protocol" {
				// Requirements are not tests.
				pending = &scope{block: true}
			} else {
				pending = &scope{names: names}
			}
			if t.text == "class" {
				if j < len(tokens) && tokens[j].text == "<" {
					j = skipBalanced(tokens, j, "<", ">") + 1
				}
				if j+1 < len(tokens) && tokens[j].text == ":" && tokens[j+1].kind == 'i' {
					super := tokens[j+1].text
					for k := j + 2; k+1 < len(tokens) && tokens[k].text == "." && tokens[k+1].kind == 'i'; k += 2 {
						super = tokens[k+1].text
					}
					s.superclass[names[len(names)-1]] = super
				}
			}
			i = j - 1
			reset()
		case t.kind == 'i' && t.text == "func" && !inBlock():
			name := next(i)
			if name.kind != 'i' {
				reset()
				continue
			}
			j := i + 2
			if j < len(tokens) && tokens[j].text == "<" {
				j = skipBalanced(tokens, j, "<", ">") + 1
			}
			if j >= len(tokens) || tokens[j].text != "(" {
				reset()
				continue
			}
			end := skipBalanced(tokens, j, "(", ")")
			labels := parameterLabels(tokens[j+1 : end])
			path := typePath()
			if contains(attrs, "Test") {
				signature := name.text + "(" + strings.Join(labels, "") + ")"
				if len(path) == 0 {
					s.ids[module+"."+signature] = true
				} else {
					s.ids[module+"."+strings.Join(path, "/")+"/"+signature] = true
				}
			} else if strings.HasPrefix(name.text, "test") && len(labels) == 0 && !static && !private && len(path) > 0 {
				s.candidates = append(s.candidates, xctestCandidate{
					module: module, class: path[len(path)-1], path: strings.Join(path, "."), method: name.text,
				})
			}
			i = end
			reset()
		case t.kind == 'i' && (t.text == "var" || t.text == "let" || t.text == "init" || t.text == "typealias" || t.text == "case" || t.text == "import"):
			reset()
		}
	}
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

// skipBalanced returns the index of the token closing the one at open.
func skipBalanced(tokens []token, open int, left, right string) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch tokens[i].text {
		case left:
			depth++
		case right:
			depth--
			if depth == 0 {
				return i
			}
		case "{", "}":
			if left == "<" {
				// Not a generic clause after all.
				return open
			}
		}
	}
	return len(tokens) - 1
}

// parameterLabels gives "label:" for each parameter, "_:" when unlabeled.
func parameterLabels(tokens []token) []string {
	var labels []string
	start, depth := 0, 0
	flush := func(param []token) {
		if len(param) == 0 {
			return
		}
		if param[0].kind == 'i' {
			labels = append(labels, param[0].text+":")
		} else {
			labels = append(labels, "_:")
		}
	}
	for i, t := range tokens {
		switch t.text {
		case "(", "[", "<":
			depth++
		case ")", "]", ">":
			depth--
		case ",":
			if depth == 0 {
				flush(skipAttributes(tokens[start:i]))
				start = i + 1
			}
		}
	}
	flush(skipAttributes(tokens[start:]))
	return labels
}

func skipAttributes(param []token) []token {
	for len(param) >= 2 && param[0].text == "@" {
		param = param[2:]
	}
	return param
}
