# Swift

Build and test cross-platform Swift packages with Dagger and Swift Package
Manager. Written with the Dang SDK, this module extracts Pulse's `iosSignals`
check into a reusable toolchain, keeping its `swift:6.0-noble` default.

This runs the shared logic used by iOS apps on Linux, including XCTest and Swift
Testing tests. Packages must support Linux, including their dependencies. Building
an iOS app or compiling code that imports UIKit, SwiftUI, or HealthKit requires
Apple SDKs on macOS.

## Install

Requires Dagger v1.0.0-beta.22 or later and Swift 6.0 or later.

```sh
dagger install github.com/dagger/apple/swift
dagger check --swift
```

Every discovered package gets a `build` check (`swift build`). Its tests form a
nested collection, with a `test` check per test function. All selected tests in
a package share one `swift test` invocation (see [Tests](#tests)). SwiftPM
handles dependency resolution. Packages without tests can be selected for the
build check alone.

## Select packages and checks

```sh
# Inspect discovered packages and checks.
dagger list swift-packages -a
dagger check --swift -l --all

# Test all packages, or just the shared iOS logic.
dagger check --swift --check test
dagger check --swift-package=mobile/ios/App/HealthSignals --check test

# Discover individual test identifiers (reads source; compiles nothing).
dagger list swift-tests -a --swift-package=mobile/ios/App/HealthSignals

# Run one test. Repeat --swift-test to select multiple tests in the package.
dagger check --swift-package=mobile/ios/App/HealthSignals \
  --swift-test='HealthSignalsTests.BaselineTests/testLeavesADayUndeterminedWithoutFiveOfThePreviousFourteen'

# Build a package that has no tests.
dagger check --swift-package=Packages/Models --check build
```

Packages are directories containing `Package.swift`. Discovery includes packages
at and below the working directory, plus the nearest enclosing package when the
working directory has no manifest. Running from a package's `Sources` or `Tests`
directory therefore selects that package. Collection keys stay relative to the
workspace root; `.` names a package at the root.

Discovery skips gitignored paths, `.build`, `node_modules`, and `DerivedData`.
Source uploads also exclude `.git` and `xcuserdata`. SwiftPM's `.swiftpm`
configuration is retained unless gitignored. Host `.build` contents are excluded
so macOS build products cannot contaminate Linux builds. Dagger caches container
execution; the module does not share a mutable `.build` cache across packages.

Use `packageInclude` and `packageExclude` to narrow automatic discovery, for
example when an iOS shell cannot resolve or compile on Linux:

```toml
[modules.swift.settings]
packageInclude = ["mobile/ios/App/HealthSignals"]
```

Alternatively, discover every visible package except the shell:

```toml
[modules.swift.settings]
packageExclude = ["mobile/ios/App/CapApp-SPM"]
```

Patterns match workspace-relative **package directories**, not `Package.swift`
filenames. Exact paths select only that package; `.` selects a root package.
Dagger globs are supported: `*` matches within a path segment, `?` matches one
character, and `**` spans directories. For example:

```toml
[modules.swift.settings]
packageInclude = ["Packages/**"]
packageExclude = ["Packages/AppleOnly/**"]
```

An empty include list includes all discovered packages. Exclusions apply after
includes and always win. Patterns remain relative to the workspace root from
nested working directories; they do not broaden the normal discovery scope or
restore gitignored or generated packages.

These settings apply to package listing and automatic build and test checks.
Excluded packages never reach `swift build` or `swift test` through discovery. Selecting an excluded or nonexistent package with
`--swift-package` fails with no checks selected. The explicit `package(ws, path)`
API remains available regardless of these filters. Filters do not prevent an
included package from resolving its own dependencies; use `includeExtraFiles`
when those dependencies need additional workspace files.

Each selected package runs in its own container. The collection helpers
`packages.build(ws)` and `packages.test(ws)` report all failing packages with the
last 30 lines of stdout and stderr. Full command output is available in the
Dagger trace.

## Tests

`package.tests(ws)` discovers identifiers from source, so listing
(`dagger check -l`, `dagger list`) runs no SwiftPM, resolves no dependencies and
compiles nothing. One scan, a small helper in a pinned image whose inputs are the
workspace's `.swift` files, serves every package. It reads test targets from
`.testTarget(name:path:)` in `Package.swift` as text (default path
`Tests/<name>`), then lists:

- XCTest: `func test…()` instance methods without parameters in classes that
  inherit from `XCTestCase` (directly, through the package's own classes, or
  through a base class whose name ends in `TestCase`), including extensions.
- Swift Testing: `@Test` functions, at top level or in any type, including
  extensions.

`#if` blocks are evaluated for Linux: `os(Linux)`, the engine's `arch`,
`canImport` (false for Apple-only frameworks), `DEBUG` unless `testFlags` select
a release configuration, and conditions passed as `-Xswiftc -DNAME` in
`testFlags`. Code that does not compile still lists; the shared run reports the
compiler's diagnostics against that package's selected tests. A package whose
files discovery cannot read lists no tests and fails its `build` check, naming
the file, without affecting other packages.

Keys use SwiftPM's identifiers, such as `SignalsTests.SignalsTests/testAnswer`
for XCTest and `SignalsTests.modernAnswer()` for Swift Testing, with nested types
joined by `/`. Parameterized functions are one item; selecting that item runs
all its argument cases. Static discovery cannot see tests produced by macros,
manifests that compute target names, inherited test methods, or code SwiftPM
excludes: a listed test that SwiftPM does not run fails as "did not run", and an
unlisted test that fails in a whole-package run fails every check of that run.

Each selected test has its own check. Every check in a package receives the
package's test collection, already filtered by the active `dagger check`
selection, and calls the same helper. The helper builds one `swift test` command
from that selection alone, so the command is identical in every check and the
engine runs it once per package. Separate packages use separate commands.

The shared run writes its reports to files instead of failing: XCTest's console
output, SwiftPM's xUnit file (XCTest results in `--parallel` mode), and Swift
Testing's JSON event stream. One indexer step, also shared, reads them once and
writes a bounded result for each selected test. Each check reads only its own
result. Failure messages keep the last 32 KiB of each diagnostic stream, so
report size never matters.

With no tests removed from the package's collection, the command runs the
package whole and `testFlags` apply unchanged. A narrowed selection passes one
`--filter` that matches the complete, sorted identifiers, escaping regular
expression characters so similarly named tests are not included. It replaces any
`--filter` or `--specifier` in `testFlags`; `--skip` still applies. Discovery
lists all tests supported by the enabled frameworks, regardless of execution
filters and skips. An empty selection runs nothing.

Results are attributed as follows:

- A failing test, or one with a failing Swift Testing issue, fails its own check.
  An issue recorded on a Swift Testing suite, such as a throwing `init`, fails
  the selected tests in that suite.
- A test that started but never finished, for example because it crashed or
  exited the test process, fails its check.
- A failure no selected check owns (a failing test discovery did not list, an
  XCTest error outside any test case, or a run-level Swift Testing issue) fails
  every check sharing the run.
- A non-zero exit with no failing or unfinished test, such as a compile error, a
  crash outside any test, or a process that exits non-zero after its tests pass,
  fails every check sharing the run, with the compiler or process output.
- A selected test with no result fails as "did not run", even when `swift test`
  passed. When `testFlags` contain `--skip`, or a whole run keeps a configured
  `--filter` or `--specifier`, such a test passes instead, since the flags may
  exclude it on purpose.

Logs and spans belong to the check that starts the shared command; the other
checks share its cached result. The module adds `--xunit-output`,
`--event-stream-output-path` and `--event-stream-version` itself, replacing any
in `testFlags`; Swift Testing's event stream requires Swift 6.0 or later.

Automatic test checks live at `swift/packages/tests/test`. The `package.test(ws)`,
`packages.test(ws)`, `tests.test(ws)` and `packages.build(ws)` helpers are plain
functions rather than checks, so `dagger check` never runs the same work twice and
selecting one test does not also run its entire package. There is no `.batch`
namespace.

## Settings

Configure the installed module in your workspace's `dagger.toml`:

```toml
[modules.swift]
source = "github.com/dagger/apple/swift"

[modules.swift.settings]
baseImageAddress = "swift:6.0-noble"
buildFlags = ["--configuration", "release"]
testFlags = ["--parallel"]
environment = ["TZ=UTC"]
includeExtraFiles = ["Packages/Shared/**", "mobile/testdata/**"]
```

| Setting | Default | Purpose |
| --- | --- | --- |
| `baseImageAddress` | `swift:6.0-noble` | Swift compiler and Linux environment. |
| `base` | unset | Prepared container with Swift and system dependencies; overrides the image. |
| `buildFlags` | `[]` | Arguments appended to `swift build`. |
| `testFlags` | `[]` | Arguments appended to `swift test`, such as `--filter`. |
| `environment` | `[]` | Environment variables as `KEY=VALUE`; spaces and additional `=` are preserved. |
| `includeExtraFiles` | `[]` | Workspace-root patterns for local dependencies and test fixtures. |
| `packageInclude` | `[]` | Workspace-relative package directory paths or globs; empty includes all discovered packages. |
| `packageExclude` | `[]` | Package directory paths or globs removed after inclusion; empty excludes none. |

The package mounts at `/src/<workspace-relative-path>` and is the working
directory. Extra files mount under `/src` at their original workspace-relative
paths, so `.package(path: "../Shared")` and relative fixture paths resolve as
they do locally. Include all sibling packages needed by the dependency graph.

Build and test flags are independent. To use a release configuration for both,
set `--configuration release` in both arrays. Arguments are passed directly to
Swift without shell interpolation.

## Compose with other modules

Add this module as a client of your Dang module, then use its package collection
or select a package explicitly:

```dang
let package = swift.package(ws, "mobile/ios/App/HealthSignals")
package.test(ws)
```

Select individual tests through the nested collection. `tests.test(ws)` runs
them in one `swift test` invocation and raises with every failing test:

```dang
let tests = swift.package(ws, "mobile/ios/App/HealthSignals").tests(ws)
tests.subset(keys: [
  "HealthSignalsTests.BaselineTests/testLeavesADayUndeterminedWithoutFiveOfThePreviousFourteen",
]).test(ws)
```

A direct call does not apply the engine's filtered view, so calling a single
test's check directly takes its selection explicitly, for example
`tests.get(key: name).test(ws, selection: tests.subset(keys: [name]))`.

The package's `container(ws)` function returns the configured container for
additional commands and artifacts:

```dang
swift.package(ws, "Packages/Models").container(ws)
  .withExec(["swift", "package", "describe"])
  .stdout
```

Pass `base` a container when a package needs extra system libraries, environment
variables, secrets, or service bindings. Those are preserved when the module adds
source and sets the package's working directory.

## Development

From the repository root:

```sh
dagger check --module e2e
dagger check --generated
dang fmt -l swift/main.dang .dagger/modules/e2e/main.dang
```

The integration suite builds real Swift packages and verifies discovery from
nested directories, collection selection, build and test failures, XCTest and
Swift Testing, local dependencies, shared fixtures, flags, environment variables,
custom base containers, and exclusion of host build products. Test collection
checks cover exact selection, similarly named tests, parameterized functions,
compilation flags, configured filters, empty selections, and compiler errors.
Package filter checks cover exact paths, globs, exclusion precedence, nested
working directories, and unresolvable dependencies.

The `selected-*` checks exercise the engine's filtered selection through the
CLI, which direct Dang calls do not apply. Each builds a fixture workspace and
runs the engine-provided, version-matched `dagger check` in an isolated session,
asserting check totals and messages: several packages, excluded tests that never
run, an isolated failure, a failing suite setup, a failure outside any selected
test, a selected test that never ran, a compile error, whole-package runs, and
`--skip` flags. `selected-one-run-per-package` counts the `swift test` and report
execs in the CLI's trace, and `selected-listing` lists a broken package with an
unpullable Swift image to prove listing runs no Swift tooling:

```sh
dagger check 'e2e/selected-*'
```

`helper-tests` runs the unit tests of the helper in `helpers/swift-tests`: the
source scan and the shared report indexer.
