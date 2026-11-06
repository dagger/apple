package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const consoleOutput = `Test Suite 'Selected tests' started at 2026-11-06 16:51:06.616
Test Suite 'SelectionCases' started at 2026-11-06 16:51:06.618
Test Case 'SelectionCases.testBar' started at 2026-11-06 16:51:06.618
Test Case 'SelectionCases.testBar' passed (0.0 seconds)
Test Case 'SelectionCases.testFoo' started at 2026-11-06 16:51:06.618
/src/Tests/SelectionTests/Cases.swift:4: error: SelectionCases.testFoo : failed - selected failure
Test Case 'SelectionCases.testFoo' failed (0.0 seconds)
Test Suite 'SelectionCases' failed at 2026-11-06 16:51:06.618
	 Executed 2 tests, with 1 failure (0 unexpected) in 0.001 (0.001) seconds
`

const eventStream = `{"kind":"test","payload":{"id":"SelectionTests.foo()\/Modern.swift:2:2","kind":"function","name":"foo()"},"version":0}
{"kind":"event","payload":{"kind":"runStarted","messages":[{"symbol":"default","text":"Test run started."}]},"version":0}
{"kind":"event","payload":{"kind":"testStarted","messages":[],"testID":"SelectionTests.foo()\/Modern.swift:2:2"},"version":0}
{"kind":"event","payload":{"kind":"testEnded","messages":[],"testID":"SelectionTests.foo()\/Modern.swift:2:2"},"version":0}
{"kind":"event","payload":{"kind":"testStarted","messages":[],"testID":"SelectionTests.Nested"},"version":0}
{"kind":"event","payload":{"kind":"testStarted","messages":[],"testID":"SelectionTests.Nested\/bar()\/Modern.swift:5:6"},"version":0}
{"kind":"event","payload":{"issue":{"isKnown":false,"sourceLocation":{"fileID":"SelectionTests\/Modern.swift","line":5,"column":30}},"kind":"issueRecorded","messages":[{"symbol":"fail","text":"Issue recorded: nested failure"}],"testID":"SelectionTests.Nested\/bar()\/Modern.swift:5:6"},"version":0}
{"kind":"event","payload":{"kind":"testEnded","messages":[],"testID":"SelectionTests.Nested\/bar()\/Modern.swift:5:6"},"version":0}
{"kind":"event","payload":{"issue":{"isKnown":true},"kind":"issueRecorded","messages":[{"symbol":"fail","text":"known"}],"testID":"SelectionTests.foo()\/Modern.swift:2:2"},"version":0}
{"kind":"event","payload":{"kind":"testSkipped","messages":[],"testID":"SelectionTests.skipped()\/Modern.swift:9:2"},"version":0}
`

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func index(t *testing.T, in inputs) map[string]string {
	t.Helper()
	results, err := summarize(in)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]string)
	for i, name := range in.selection.Names {
		byName[name] = results[i]
	}
	return byName
}

func TestAttribution(t *testing.T) {
	results := index(t, inputs{
		selection: selection{Names: []string{
			"SelectionTests.Nested/bar()",
			"SelectionTests.SelectionCases/testBar",
			"SelectionTests.SelectionCases/testFoo",
			"SelectionTests.foo()",
			"SelectionTests.missing()",
			"SelectionTests.skipped()",
		}},
		stdout:   write(t, "stdout", consoleOutput),
		events:   write(t, "events", eventStream),
		exitCode: 1,
	})
	for name, want := range map[string]string{
		"SelectionTests.Nested/bar()":           "nested failure",
		"SelectionTests.SelectionCases/testBar": "",
		"SelectionTests.SelectionCases/testFoo": "selected failure",
		"SelectionTests.foo()":                  "",
		"SelectionTests.missing()":              "did not run",
		"SelectionTests.skipped()":              "",
	} {
		got := results[name]
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestAllowUnrun(t *testing.T) {
	results := index(t, inputs{
		selection: selection{Names: []string{"SelectionTests.missing()"}, AllowUnrun: true},
		events:    write(t, "events", ""),
	})
	if got := results["SelectionTests.missing()"]; got != "" {
		t.Errorf("unrun test failed despite configured filters: %q", got)
	}
}

func TestFailureOutsideSelectedTests(t *testing.T) {
	output := "Test Case 'SelectionCases.testOther' started at x\nboom\nTest Case 'SelectionCases.testOther' failed (0.0 seconds)\n"
	results := index(t, inputs{
		selection: selection{Names: []string{"SelectionTests.SelectionCases/testBar"}},
		stdout:    write(t, "stdout", output+strings.Replace(consoleOutput, "testFoo", "testBaz", -1)),
		exitCode:  1,
	})
	got := results["SelectionTests.SelectionCases/testBar"]
	if !strings.Contains(got, "failures outside the selected test checks: SelectionCases.testBaz, SelectionCases.testOther") || !strings.Contains(got, "boom") {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestExitWithoutTestFailure(t *testing.T) {
	results := index(t, inputs{
		selection: selection{Names: []string{"SelectionTests.SelectionCases/testBar"}},
		stdout:    write(t, "stdout", strings.SplitAfter(consoleOutput, "passed (0.0 seconds)\n")[0]),
		stderr:    write(t, "stderr", "error: Exited with unexpected signal code 3\n"),
		exitCode:  1,
	})
	if got := results["SelectionTests.SelectionCases/testBar"]; !strings.Contains(got, "swift test failed outside any test (exit 1)") || !strings.Contains(got, "signal code 3") {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestUnfinishedTest(t *testing.T) {
	results := index(t, inputs{
		selection: selection{Names: []string{"SelectionTests.SelectionCases/testFoo", "SelectionTests.SelectionCases/testQux"}},
		stdout:    write(t, "stdout", "Test Case 'SelectionCases.testFoo' started at x\nFatal error: crash\n"),
		exitCode:  1,
	})
	if got := results["SelectionTests.SelectionCases/testFoo"]; !strings.Contains(got, "did not finish") || !strings.Contains(got, "Fatal error: crash") {
		t.Errorf("unexpected crash result: %q", got)
	}
	if got := results["SelectionTests.SelectionCases/testQux"]; !strings.Contains(got, "did not run") {
		t.Errorf("unexpected unrun result: %q", got)
	}
}

func TestParallelXUnit(t *testing.T) {
	xunit := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites><testsuite name="TestResults" errors="0" tests="2" failures="1">
<testcase classname="SelectionTests.SelectionCases" name="testBar" time="0.1"></testcase>
<testcase classname="SelectionTests.SelectionCases" name="testFoo" time="0.1"><failure message="failed"></failure></testcase>
<testcase classname="SelectionTests" name="foo()" time="0.1"><failure message="ignored: Swift Testing"></failure></testcase>
</testsuite></testsuites>`
	results := index(t, inputs{
		selection: selection{Names: []string{"SelectionTests.SelectionCases/testBar", "SelectionTests.SelectionCases/testFoo"}},
		stdout:    write(t, "stdout", "[1/2] Testing SelectionTests.SelectionCases/testBar\n"),
		xunit:     write(t, "xunit", xunit),
		exitCode:  1,
	})
	if got := results["SelectionTests.SelectionCases/testBar"]; got != "" {
		t.Errorf("passing parallel test failed: %q", got)
	}
	if got := results["SelectionTests.SelectionCases/testFoo"]; !strings.Contains(got, "testFoo failed") {
		t.Errorf("failing parallel test passed: %q", got)
	}
}

func TestBoundedResults(t *testing.T) {
	huge := "Test Case 'SelectionCases.testFoo' started at x\n" + strings.Repeat("x", 1<<20) + "\nTest Case 'SelectionCases.testFoo' failed (0.0 seconds)\n"
	results := index(t, inputs{
		selection: selection{Names: []string{"SelectionTests.SelectionCases/testFoo"}},
		stdout:    write(t, "stdout", huge),
		exitCode:  1,
	})
	if got := results["SelectionTests.SelectionCases/testFoo"]; len(got) > 3*diagnosticLimit+64 || !strings.Contains(got, "earlier output omitted") {
		t.Errorf("result was not bounded: %d bytes", len(got))
	}
}
