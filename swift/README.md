# Swift

Build and test cross-platform Swift packages with Dagger and Swift Package
Manager. Written with the Dang SDK, this module extracts Pulse's `iosSignals`
check into a reusable toolchain, keeping its `swift:6.0-noble` default.

This runs the shared logic used by iOS apps on Linux, including XCTest and Swift
Testing tests. Packages must support Linux, including their dependencies. Building
an iOS app or compiling code that imports UIKit, SwiftUI, or HealthKit requires
Apple SDKs on macOS.

## Install

Requires Dagger v1.0.0-beta.21.

```sh
dagger install github.com/dagger/apple/swift
dagger check --swift
```

Every discovered package gets a `build` check (`swift build`). Its tests form a
nested collection, with a `test` check per test function. Selected tests run
together in one `swift test` invocation per package. SwiftPM handles dependency
resolution. Packages without tests can be selected for the build check alone.

## Select packages and checks

```sh
# Inspect discovered packages and checks.
dagger list swift-packages -a
dagger check --swift -l --all

# Test all packages, or just the shared iOS logic.
dagger check --swift --check test
dagger check --swift-package=mobile/ios/App/HealthSignals --check test

# Discover individual test identifiers (compiles the test targets).
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

Each selected package runs in its own container. Batch checks report all failing
packages with the last 30 lines of stdout and stderr. Full command output is available
in the Dagger trace.

## Test collections

`package.tests(ws)` discovers identifiers with `swift test list`, covering XCTest
and Swift Testing. Unlike package discovery, listing tests resolves dependencies
and compiles the test targets. Compiler and discovery failures are reported as
errors. Execution reuses those compiled artifacts.

Keys use SwiftPM's identifiers, such as `SignalsTests.SignalsTests/testAnswer`
for XCTest and `SignalsTests.modernAnswer()` for Swift Testing. Parameterized
functions are one item; selecting that item runs all its argument cases.
Overloads and otherwise ambiguous Swift Testing names use the source-location
suffix reported by SwiftPM.

Selecting individual tests matches complete identifiers, escaping regular
expression characters so similarly named tests are not included. A narrowed
selection replaces any `--filter` or `--specifier` in `testFlags`; `--skip` still
applies. With the entire collection selected, `testFlags` apply unchanged.
Discovery lists all tests supported by the enabled frameworks, regardless of
execution filters and skips. An empty selection runs nothing.

Build options in `testFlags` also apply to discovery. Run-only options, such as
`--parallel`, `--num-workers`, `--filter`, and `--xunit-output`, are omitted from
the listing command because SwiftPM does not accept them there.

Automatic test checks live at `swift/packages/tests/test`. The direct
`package.test(ws)` and `packages.batch.test(ws)` helpers still run whole packages,
but are not automatic checks, so selecting one test does not also run its entire
package.

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

Select individual tests through the nested collection:

```dang
let tests = swift.package(ws, "mobile/ios/App/HealthSignals").tests(ws)
let result = tests.subset(keys: [
  "HealthSignalsTests.BaselineTests/testLeavesADayUndeterminedWithoutFiveOfThePreviousFourteen",
]).batch.test(ws)
if (result.pass == false) { raise result.error.message ?? "Swift tests failed" }
```

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
compilation flags, configured filters, empty selections, and discovery errors.
