# Apple development modules for Dagger

Reusable Dagger modules for Apple development workflows.

| Module | Description |
| --- | --- |
| [Swift](swift/README.md) | Build and test cross-platform Swift packages in Linux containers. |

Requires Dagger v1.0.0-beta.21.

```sh
dagger install github.com/dagger/apple/swift
dagger check --swift
```

To develop the modules in this repository:

```sh
dagger check --module e2e
dagger check --generated
```
