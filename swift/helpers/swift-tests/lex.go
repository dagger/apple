package main

import (
	"runtime"
	"strings"
)

// lex splits Swift source into the tokens the scan needs, dropping comments,
// string contents and code in inactive conditional compilation blocks.
func lex(src []byte, conditions map[string]bool) []token {
	l := &lexer{src: src, conditions: conditions, lineStart: true}
	l.run()
	return l.tokens
}

type condition struct{ parentActive, taken, active bool }

type lexer struct {
	src        []byte
	i          int
	conditions map[string]bool
	stack      []condition
	lineStart  bool
	tokens     []token
}

func (l *lexer) active() bool {
	return len(l.stack) == 0 || l.stack[len(l.stack)-1].active
}

func (l *lexer) emit(kind byte, text string) {
	if l.active() {
		l.tokens = append(l.tokens, token{kind, text})
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func (l *lexer) run() {
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '\n':
			l.lineStart = true
			l.i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			l.i++
			continue
		case c == '/' && l.peek(1) == '/':
			for l.i < len(l.src) && l.src[l.i] != '\n' {
				l.i++
			}
			continue
		case c == '/' && l.peek(1) == '*':
			l.blockComment()
			continue
		case c == '#' && l.lineStart && l.directive():
			continue
		}
		l.lineStart = false
		switch {
		case c == '"' || (c == '#' && l.rawStringAhead()):
			l.str()
			l.emit('s', "")
		case c == '`':
			end := l.i + 1
			for end < len(l.src) && l.src[end] != '`' && l.src[end] != '\n' {
				end++
			}
			l.emit('i', string(l.src[l.i+1:end]))
			l.i = end + 1
		case isIdentStart(c):
			start := l.i
			for l.i < len(l.src) && isIdentPart(l.src[l.i]) {
				l.i++
			}
			l.emit('i', string(l.src[start:l.i]))
		case c >= '0' && c <= '9':
			for l.i < len(l.src) && (isIdentPart(l.src[l.i]) || l.src[l.i] == '.' && l.peek(1) >= '0' && l.peek(1) <= '9') {
				l.i++
			}
			l.emit('n', "")
		case c == '-' && l.peek(1) == '>':
			l.emit('p', "->")
			l.i += 2
		default:
			l.emit('p', string(c))
			l.i++
		}
	}
}

func (l *lexer) peek(n int) byte {
	if l.i+n < len(l.src) {
		return l.src[l.i+n]
	}
	return 0
}

func (l *lexer) blockComment() {
	depth := 0
	for l.i < len(l.src) {
		if l.src[l.i] == '/' && l.peek(1) == '*' {
			depth++
			l.i += 2
		} else if l.src[l.i] == '*' && l.peek(1) == '/' {
			depth--
			l.i += 2
			if depth == 0 {
				return
			}
		} else {
			l.i++
		}
	}
}

func (l *lexer) rawStringAhead() bool {
	j := l.i
	for j < len(l.src) && l.src[j] == '#' {
		j++
	}
	return j < len(l.src) && l.src[j] == '"'
}

// str skips a string literal: plain, multi-line and raw, with interpolations.
func (l *lexer) str() {
	hashes := 0
	for l.src[l.i] == '#' {
		hashes++
		l.i++
	}
	quotes := 1
	if l.peek(1) == '"' && l.peek(2) == '"' {
		quotes = 3
	}
	l.i += quotes
	closing := strings.Repeat(`"`, quotes) + strings.Repeat("#", hashes)
	escape := `\` + strings.Repeat("#", hashes)
	for l.i < len(l.src) {
		if strings.HasPrefix(string(l.src[l.i:min(len(l.src), l.i+len(closing))]), closing) {
			l.i += len(closing)
			return
		}
		if strings.HasPrefix(string(l.src[l.i:min(len(l.src), l.i+len(escape))]), escape) {
			l.i += len(escape)
			if l.i < len(l.src) && l.src[l.i] == '(' {
				l.interpolation()
			} else {
				l.i++
			}
			continue
		}
		if quotes == 1 && l.src[l.i] == '\n' {
			return
		}
		l.i++
	}
}

// interpolation skips \( ... ), which may hold nested strings.
func (l *lexer) interpolation() {
	depth := 0
	for l.i < len(l.src) {
		switch c := l.src[l.i]; {
		case c == '(':
			depth++
			l.i++
		case c == ')':
			depth--
			l.i++
			if depth == 0 {
				return
			}
		case c == '"' || (c == '#' && l.rawStringAhead()):
			l.str()
		default:
			l.i++
		}
	}
}

// directive handles #if, #elseif, #else and #endif at the start of a line.
func (l *lexer) directive() bool {
	end := l.i
	for end < len(l.src) && l.src[end] != '\n' {
		end++
	}
	line := string(l.src[l.i:end])
	if c := strings.Index(line, "//"); c >= 0 {
		line = line[:c]
	}
	word, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	parentActive := func() bool {
		if len(l.stack) == 0 {
			return true
		}
		return l.stack[len(l.stack)-1].parentActive
	}
	switch word {
	case "#if":
		parent := l.active()
		v := evaluate(rest, l.conditions)
		l.stack = append(l.stack, condition{parentActive: parent, taken: v, active: parent && v})
	case "#elseif":
		if len(l.stack) == 0 {
			return false
		}
		top := &l.stack[len(l.stack)-1]
		if top.taken {
			top.active = false
		} else {
			v := evaluate(rest, l.conditions)
			top.taken, top.active = v, parentActive() && v
		}
	case "#else":
		if len(l.stack) == 0 {
			return false
		}
		top := &l.stack[len(l.stack)-1]
		top.active = parentActive() && !top.taken
		top.taken = true
	case "#endif":
		if len(l.stack) > 0 {
			l.stack = l.stack[:len(l.stack)-1]
		}
	default:
		return false
	}
	l.i = end
	return true
}

// Modules that exist only on Apple platforms, for canImport.
var appleOnly = map[string]bool{
	"Darwin": true, "UIKit": true, "AppKit": true, "SwiftUI": true, "Combine": true, "CoreData": true,
	"HealthKit": true, "WatchKit": true, "ObjectiveC": true, "CoreGraphics": true, "CoreLocation": true,
	"MapKit": true, "StoreKit": true, "Cocoa": true, "os": true, "OSLog": true, "Security": true,
}

// evaluate decides a conditional compilation expression for a Linux build.
// Unknown checks count as true, so the scan errs towards listing a test,
// which the shared run then reports as not run.
func evaluate(expr string, conditions map[string]bool) bool {
	p := &exprParser{tokens: lex([]byte(expr), nil), conditions: conditions}
	return p.or()
}

type exprParser struct {
	tokens     []token
	i          int
	conditions map[string]bool
}

func (p *exprParser) peek() string {
	if p.i < len(p.tokens) {
		return p.tokens[p.i].text
	}
	return ""
}

func (p *exprParser) or() bool {
	v := p.and()
	for p.peek() == "|" {
		p.i += 2
		v = p.and() || v
	}
	return v
}

func (p *exprParser) and() bool {
	v := p.unary()
	for p.peek() == "&" {
		p.i += 2
		v = p.unary() && v
	}
	return v
}

func (p *exprParser) unary() bool {
	switch p.peek() {
	case "!":
		p.i++
		return !p.unary()
	case "(":
		p.i++
		v := p.or()
		if p.peek() == ")" {
			p.i++
		}
		return v
	}
	if p.i >= len(p.tokens) {
		return true
	}
	name := p.tokens[p.i].text
	p.i++
	if p.peek() != "(" {
		switch name {
		case "true":
			return true
		case "false":
			return false
		}
		return p.conditions[name]
	}
	start := p.i + 1
	depth := 0
	for ; p.i < len(p.tokens); p.i++ {
		if p.tokens[p.i].text == "(" {
			depth++
		} else if p.tokens[p.i].text == ")" {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	var arg string
	if start < p.i && start < len(p.tokens) {
		arg = p.tokens[start].text
	}
	p.i++
	switch name {
	case "os":
		return arg == "Linux"
	case "canImport":
		return !appleOnly[arg]
	case "arch":
		arch := map[string]string{"amd64": "x86_64", "arm64": "arm64", "386": "i386", "arm": "arm"}[runtime.GOARCH]
		return arg == arch || (arch == "arm64" && arg == "aarch64")
	case "targetEnvironment":
		return false
	case "_runtime":
		return arg == "_Native"
	}
	// swift(), compiler(), hasFeature(), hasAttribute(), _endian()...
	return true
}
