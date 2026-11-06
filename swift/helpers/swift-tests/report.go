// The report command reads the reports of one shared swift test run once and
// writes a bounded failure message (or an empty passing result) for each
// selected test check.
//
// swift test runs XCTest and then Swift Testing. Their results come from:
//
//   - Swift Testing: the JSON event stream (--event-stream-output-path,
//     --event-stream-version 0), keyed by test ID.
//   - XCTest: the xUnit file SwiftPM writes in --parallel mode, and otherwise
//     XCTest's own console protocol on stdout ("Test Case 'C.m' started",
//     "... passed/failed/skipped"). XCTest has no other machine-readable report
//     on Linux.
package main

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const diagnosticLimit = 32 << 10

// tail keeps diagnostic memory bounded even when a passing test logs gigabytes.
type tail struct {
	data      []byte
	truncated bool
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if n >= diagnosticLimit {
		t.truncated = t.truncated || len(t.data) > 0 || n > diagnosticLimit
		t.data = append(t.data[:0], p[n-diagnosticLimit:]...)
	} else {
		if extra := len(t.data) + n - diagnosticLimit; extra > 0 {
			t.data = append(t.data[:0], t.data[extra:]...)
			t.truncated = true
		}
		t.data = append(t.data, p...)
	}
	return n, nil
}

func (t *tail) add(s string) { _, _ = t.Write([]byte(s)) }

func (t *tail) line(s string) { t.add(s + "\n") }

func (t *tail) String() string {
	if t.truncated {
		return "[earlier output omitted; showing diagnostic tail]\n" + string(t.data)
	}
	return string(t.data)
}

type selection struct {
	// Names are SwiftPM test identifiers, sorted in byte order by the caller.
	// Result files are named by their index in this list.
	Names []string `json:"names"`
	// AllowUnrun is set when the configured testFlags (--skip, or --filter on
	// a whole run) may legitimately exclude a selected test.
	AllowUnrun bool `json:"allowUnrun"`
}

type testResult struct {
	observed, failed, finished bool
	log                        tail
}

// location matches the source-location component Swift Testing appends to a
// function's ID, and that `swift test list` prints only for ambiguous names.
var location = regexp.MustCompile(`/[^/]+\.swift:\d+:\d+$`)

func withoutLocation(id string) string { return location.ReplaceAllString(id, "") }

// isSwiftTesting reports whether a SwiftPM identifier names a Swift Testing
// function ("Module.f()", "Module.Suite/f(x:)") rather than an XCTest method
// ("Module.Class/testF").
func isSwiftTesting(id string) bool { return strings.HasSuffix(withoutLocation(id), ")") }

// xctestNames gives the names XCTest prints for "Module.Class/method":
// corelibs-xctest prints "Class.method"; the module-qualified form is
// accepted too.
func xctestNames(id string) []string {
	class, method, ok := strings.Cut(id, "/")
	if !ok {
		return nil
	}
	names := []string{class + "." + method}
	if _, unqualified, ok := strings.Cut(class, "."); ok {
		names = append(names, unqualified+"."+method)
	}
	return names
}

type summary struct {
	tests map[string]*testResult
	// Swift Testing IDs and XCTest names mapped to selected identifiers.
	swiftTesting map[string][]string
	xctest       map[string][]string
	// Failures that no selected test check owns.
	unchecked     []string
	uncheckedLog  tail
	anyFailure    bool
	selectedNames []string
}

func newSummary(names []string) (*summary, error) {
	s := &summary{
		tests:         make(map[string]*testResult),
		swiftTesting:  make(map[string][]string),
		xctest:        make(map[string][]string),
		selectedNames: names,
	}
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("invalid selected test %q", name)
		}
		if _, exists := s.tests[name]; exists {
			return nil, fmt.Errorf("duplicate selected test %q", name)
		}
		s.tests[name] = &testResult{}
		if isSwiftTesting(name) {
			s.swiftTesting[name] = append(s.swiftTesting[name], name)
		} else {
			for _, x := range xctestNames(name) {
				s.xctest[x] = append(s.xctest[x], name)
			}
		}
	}
	return s, nil
}

func (s *summary) uncheckedFailure(name, log string) {
	s.anyFailure = true
	s.unchecked = append(s.unchecked, name)
	s.uncheckedLog.add(log)
}

// swiftTestingTargets resolves an event's test ID to selected identifiers.
// A suite's ID covers every selected function inside it.
func (s *summary) swiftTestingTargets(id string) (targets []string, function bool) {
	if t := s.swiftTesting[id]; t != nil {
		return t, true
	}
	if t := s.swiftTesting[withoutLocation(id)]; t != nil {
		return t, true
	}
	if location.MatchString(id) || strings.HasSuffix(id, ")") {
		return nil, true
	}
	for _, name := range s.selectedNames {
		if isSwiftTesting(name) && strings.HasPrefix(name, id+"/") {
			targets = append(targets, name)
		}
	}
	return targets, false
}

type message struct {
	Symbol string `json:"symbol"`
	Text   string `json:"text"`
}

type streamRecord struct {
	Kind    string `json:"kind"`
	Payload struct {
		Kind     string    `json:"kind"`
		TestID   *string   `json:"testID"`
		Messages []message `json:"messages"`
		Issue    *struct {
			IsKnown        bool   `json:"isKnown"`
			Severity       string `json:"severity"`
			SourceLocation *struct {
				FileID string `json:"fileID"`
				Line   int    `json:"line"`
				Column int    `json:"column"`
			} `json:"sourceLocation"`
		} `json:"issue"`
	} `json:"payload"`
}

func (s *summary) readEvents(r io.Reader) error {
	decoder := json.NewDecoder(r)
	for {
		var record streamRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode Swift Testing event stream: %w", err)
		}
		if record.Kind != "event" {
			continue
		}
		p := record.Payload
		var text strings.Builder
		for _, m := range p.Messages {
			text.WriteString(m.Text + "\n")
		}
		failing := p.Kind == "issueRecorded" && p.Issue != nil && !p.Issue.IsKnown && p.Issue.Severity != "warning"
		if failing && p.Issue.SourceLocation != nil {
			loc := p.Issue.SourceLocation
			text.WriteString(fmt.Sprintf("  at %s:%d:%d\n", loc.FileID, loc.Line, loc.Column))
		}
		if p.TestID == nil || *p.TestID == "" {
			if failing {
				s.uncheckedFailure("Swift Testing run", text.String())
			}
			continue
		}
		targets, function := s.swiftTestingTargets(*p.TestID)
		if len(targets) == 0 {
			if failing {
				s.uncheckedFailure(*p.TestID, text.String())
			}
			continue
		}
		if failing {
			s.anyFailure = true
		}
		for _, name := range targets {
			t := s.tests[name]
			t.log.add(text.String())
			t.failed = t.failed || failing
			if !function {
				// A suite's issues fail its tests; its start and end do not run them.
				continue
			}
			switch p.Kind {
			case "testStarted":
				t.observed = true
			case "testEnded", "testSkipped":
				t.observed = true
				t.finished = true
			}
		}
	}
}

var (
	caseStarted = regexp.MustCompile(`^Test Case '([^']+)' started`)
	caseEnded   = regexp.MustCompile(`^Test Case '([^']+)' (passed|failed|skipped)`)
	// "<file>:<line>: error: <Class or Class.method> : <message>"
	caseError = regexp.MustCompile(`:\d+: error: \S+ : `)
)

// readXCTestConsole follows XCTest's console protocol. Lines between a test
// case's start and end belong to that test.
func (s *summary) readXCTestConsole(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	current := ""
	var orphan tail
	for scanner.Scan() {
		line := scanner.Text()
		if m := caseStarted.FindStringSubmatch(line); m != nil {
			current = m[1]
			for _, name := range s.xctest[current] {
				s.tests[name].observed = true
				s.tests[name].log.line(line)
			}
			continue
		}
		if m := caseEnded.FindStringSubmatch(line); m != nil {
			failed := m[2] == "failed"
			targets := s.xctest[m[1]]
			if failed {
				s.anyFailure = true
				if len(targets) == 0 {
					s.uncheckedFailure(m[1], orphan.String()+line+"\n")
				}
			}
			for _, name := range targets {
				t := s.tests[name]
				t.observed = true
				t.finished = true
				t.failed = t.failed || failed
				t.log.line(line)
			}
			current = ""
			orphan = tail{}
			continue
		}
		if current != "" {
			targets := s.xctest[current]
			for _, name := range targets {
				s.tests[name].log.line(line)
			}
			if len(targets) == 0 {
				orphan.line(line)
			}
			continue
		}
		// An XCTest error outside any test case, such as a failing class setUp.
		if caseError.MatchString(line) {
			s.uncheckedFailure("XCTest outside a test case", line+"\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read swift test stdout: %w", err)
	}
	return nil
}

type xunitCase struct {
	ClassName string `xml:"classname,attr"`
	Name      string `xml:"name,attr"`
	Failure   *struct {
		Message string `xml:"message,attr"`
		Body    string `xml:",chardata"`
	} `xml:"failure"`
	Skipped *struct{} `xml:"skipped"`
}

// readXCTestXUnit reads the xUnit file SwiftPM writes for XCTest in parallel
// mode, where the console shows only failing tests' output.
func (s *summary) readXCTestXUnit(r io.Reader) error {
	decoder := xml.NewDecoder(r)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode xUnit report: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var c xunitCase
		if err := decoder.DecodeElement(&c, &start); err != nil {
			return fmt.Errorf("decode xUnit test case: %w", err)
		}
		if strings.HasSuffix(c.Name, ")") {
			// Swift Testing results come from the event stream.
			continue
		}
		id := c.ClassName + "/" + c.Name
		failed := c.Failure != nil
		if failed {
			s.anyFailure = true
		}
		t := s.tests[id]
		if t == nil {
			if failed {
				s.uncheckedFailure(id, c.Failure.Message+"\n"+c.Failure.Body)
			}
			continue
		}
		t.observed = true
		t.finished = true
		if failed {
			t.failed = true
			t.log.line(strings.TrimSpace(c.Failure.Message + "\n" + c.Failure.Body))
		}
	}
}

func openOptional(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

type inputs struct {
	selection                     selection
	stdout, stderr, events, xunit string
	exitCode                      int
}

func summarize(in inputs) ([]string, error) {
	s, err := newSummary(in.selection.Names)
	if err != nil {
		return nil, err
	}
	readers := []struct {
		path string
		read func(io.Reader) error
	}{
		{in.events, s.readEvents},
		{in.xunit, s.readXCTestXUnit},
		{in.stdout, s.readXCTestConsole},
	}
	for _, reader := range readers {
		f, err := openOptional(reader.path)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		err = reader.read(f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	var stdoutTail, stderrTail tail
	for _, stream := range []struct {
		path string
		tail *tail
	}{{in.stdout, &stdoutTail}, {in.stderr, &stderrTail}} {
		f, err := openOptional(stream.path)
		if err != nil {
			return nil, err
		}
		if f != nil {
			_, err = io.Copy(stream.tail, f)
			f.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	outputs := "stdout:\n" + stdoutTail.String() + "\nstderr:\n" + stderrTail.String()
	unfinished := false
	for _, t := range s.tests {
		unfinished = unfinished || (t.observed && !t.finished)
	}
	globalFailure := ""
	if len(s.unchecked) > 0 {
		names := append([]string(nil), s.unchecked...)
		sort.Strings(names)
		globalFailure = "failures outside the selected test checks: " + strings.Join(uniq(names), ", ") + "\n" + s.uncheckedLog.String()
	} else if in.exitCode != 0 && !s.anyFailure && !unfinished {
		// A build failure, a crash outside any test, or a failing teardown.
		globalFailure = fmt.Sprintf("swift test failed outside any test (exit %d)\n%s", in.exitCode, outputs)
	}
	results := make([]string, len(in.selection.Names))
	for i, name := range in.selection.Names {
		t := s.tests[name]
		failure := ""
		switch {
		case t.failed:
			failure = name + " failed\n" + t.log.String()
		case globalFailure != "":
			failure = globalFailure
		case t.finished:
		case t.observed:
			failure = fmt.Sprintf("%s did not finish (swift test exit %d)\n%s\n%s", name, in.exitCode, t.log.String(), outputs)
		case in.selection.AllowUnrun:
			// testFlags' --skip or --filter may have excluded it.
		default:
			failure = fmt.Sprintf("%s did not run: swift test (exit %d) reported no result for it\n%s", name, in.exitCode, outputs)
		}
		// Headers and combined logs are bounded too, so every result stays
		// comfortably below File.contents' limit.
		if len(failure) > 3*diagnosticLimit {
			failure = failure[:3*diagnosticLimit] + "\n[remaining diagnostics omitted]\n"
		}
		results[i] = failure
	}
	return results, nil
}

func uniq(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func runReport(args []string) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	selectionPath := flags.String("selection", "", "selected test identifiers")
	stdoutPath := flags.String("stdout", "", "swift test stdout")
	stderrPath := flags.String("stderr", "", "swift test stderr")
	eventsPath := flags.String("events", "", "Swift Testing event stream (optional)")
	xunitPath := flags.String("xunit", "", "XCTest xUnit report (optional)")
	exitCode := flags.Int("exit-code", 0, "swift test exit code")
	output := flags.String("output", "", "result directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*selectionPath)
	if err != nil {
		return err
	}
	var sel selection
	if err := json.Unmarshal(data, &sel); err != nil {
		return err
	}
	results, err := summarize(inputs{
		selection: sel,
		stdout:    *stdoutPath,
		stderr:    *stderrPath,
		events:    *eventsPath,
		xunit:     *xunitPath,
		exitCode:  *exitCode,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		return err
	}
	// Results are named by rank in the caller's sorted list, so identifiers
	// never become file paths.
	for i, result := range results {
		if err := os.WriteFile(filepath.Join(*output, fmt.Sprint(i)), []byte(result), 0o644); err != nil {
			return err
		}
	}
	return nil
}
