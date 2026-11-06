package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const manifest = `// swift-tools-version: 5.9
import PackageDescription
let package = Package(
    name: "Signals", // name: "NotATarget"
    targets: [
        .target(name: "Signals"),
        .testTarget(name: "SignalsTests", dependencies: [.product(name: "X", package: "x")]),
        .testTarget(
            name: "Other-Tests",
            path: "Custom/Other"
        ),
    ]
)
`

func TestScanIdentifiers(t *testing.T) {
	root := writeTree(t, map[string]string{
		"Package.swift": manifest,
		"Tests/SignalsTests/SignalsTests.swift": `import XCTest
@testable import Signals
class Base: XCTestCase {
    func testInBase() {}
    func helper() {}
}
final class SignalsTests: Base {
    func testAnswer() throws { XCTAssertEqual(answer(), 42) }
    func testWithArg(_ x: Int) {}
    private func testPrivate() {}
    class func testClassMethod() {}
    func testAsync() async throws {
        struct Local { func testLocal() {} }
        let s = "func testInString() {}"
        _ = s
    }
}
extension SignalsTests {
    func testInExtension() {}
}
final class NotATest: NSObject {
    func testNope() {}
}
protocol P { func testRequirement() }
/* func testComment() {} */
`,
		"Tests/SignalsTests/Modern.swift": `import Testing
@Test func modern() { #expect(2 + 2 == 4) }
@Test("Display", .tags(.fast)) func named() {}
@Test(arguments: [1, 2]) func parameters(value: Int) {}
@Test(arguments: zip([1], [2]))
func pairs(_ a: Int, b c: Int) {}
@Test func closureParam(f: (Int) -> Void = { _ in }, g: Int = 1) {}
@Suite struct Nested {
    @Test func nested() {}
    struct Inner {
        @Test @MainActor func inner() async {}
    }
}
extension Nested.Inner {
    @Test func extended() {}
}
#if EXTRA_TEST
@Test func conditional() {}
#elseif os(Linux)
@Test func linux() {}
#else
@Test func other() {}
#endif
#if canImport(UIKit)
@Test func ios() {}
#endif
let multi = """
  @Test func inMultiline() {}
  """
`,
		"Custom/Other/OtherTests.swift": `import XCTest
final class OtherTests: XCTestCase { func testOther() {} }
`,
		"Tests/Unlisted/UnlistedTests.swift": `import XCTest
final class UnlistedTests: XCTestCase { func testUnlisted() {} }
`,
	})
	got := scanPackage(root, ".", map[string]bool{"DEBUG": true})
	want := []string{
		"Other_Tests.OtherTests/testOther",
		"SignalsTests.Base/testInBase",
		"SignalsTests.Nested/Inner/extended()",
		"SignalsTests.Nested/Inner/inner()",
		"SignalsTests.Nested/nested()",
		"SignalsTests.SignalsTests/testAnswer",
		"SignalsTests.SignalsTests/testAsync",
		"SignalsTests.SignalsTests/testInExtension",
		"SignalsTests.closureParam(f:g:)",
		"SignalsTests.linux()",
		"SignalsTests.modern()",
		"SignalsTests.named()",
		"SignalsTests.pairs(_:b:)",
		"SignalsTests.parameters(value:)",
	}
	if got.Error != "" || !reflect.DeepEqual(got.Tests, want) {
		t.Errorf("got %q (error %q)\nwant %q", got.Tests, got.Error, want)
	}
	defined := scanPackage(root, ".", map[string]bool{"EXTRA_TEST": true})
	if !contains(defined.Tests, "SignalsTests.conditional()") || contains(defined.Tests, "SignalsTests.linux()") {
		t.Errorf("compilation conditions ignored: %q", defined.Tests)
	}
}

func TestScanRoots(t *testing.T) {
	root := writeTree(t, map[string]string{
		"Package.swift":                        "",
		"apps/one/Package.swift":               "",
		".build/checkouts/dep/Package.swift":   "",
		"node_modules/dep/Package.swift":       "",
		"apps/one/.build/x/Package.swift":      "",
		"apps/two/Sources/Lib/Lib.swift":       "",
		"apps/two/Package.swift":               "",
		"apps/two/DerivedData/Package.swift":   "",
		"apps/two/Tests/Fixture/Package.swift": "",
	})
	roots, err := packageRoots(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "apps/one", "apps/two", "apps/two/Tests/Fixture"}
	if !reflect.DeepEqual(roots, want) {
		t.Errorf("got %q, want %q", roots, want)
	}
}

func TestScanBrokenSource(t *testing.T) {
	// Code that does not compile still lists: compiling is the run's job.
	root := writeTree(t, map[string]string{
		"Package.swift":                   manifest,
		"Tests/SignalsTests/Broken.swift": "import Testing\n@Test func broken() { undefinedSymbol( }\n",
	})
	got := scanPackage(root, ".", nil)
	if got.Error != "" || !reflect.DeepEqual(got.Tests, []string{"SignalsTests.broken()"}) {
		t.Errorf("got %q (error %q)", got.Tests, got.Error)
	}
}
