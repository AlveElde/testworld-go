# testworld-go

`testworld-go` is a system test framework based on `testcontainers-go`. Testcontainers is very flexible, but needs a lot of boilerplate for every test. Testworld cuts down on the boilerplate with an opinionated approach.

## Features

- **Shared Network**: All containers in a World share a Docker bridge network and can communicate by name
- **Automatic Cleanup**: Containers are automatically terminated when the World is destroyed
- **World Logging**: Optional logging with Gantt chart visualization of container events
- **Simplified API**: Reduced boilerplate compared to raw testcontainers-go

## Installation

```bash
go get github.com/AlveElde/testworld-go
```

## Quick Start

```go
package mytest

import (
    "testing"

    "github.com/AlveElde/testworld-go"
    "github.com/testcontainers/testcontainers-go/wait"
)

func TestExample(t *testing.T) {
    // Create a new test world
    w := testworld.New(t, "")
    defer w.Destroy()

    // Define a container
    spec := testworld.ContainerSpec{
        Image:   "varnish/orca:latest",
        Started: true,
        WaitingFor: wait.ForHTTP("/readyz").WithPort("80/tcp"),
    }

    // Create and start the container
    orca := w.NewContainer(spec)

    // Execute a command in the container
    orca.Exec([]string{"varnish-supervisor", "--version"}, 0)
}
```

## API Reference

### World

```go
// Create a new World. Pass a directory path to enable logging, or "" to disable.
w := testworld.New(t, "/path/to/logs")
defer w.Destroy()
```

### ContainerSpec

```go
spec := testworld.ContainerSpec{
    // Image to use (e.g., "alpine:latest")
    Image: "alpine:latest",

    // Or build from Dockerfile
    FromDockerfile: testcontainers.FromDockerfile{
        Context: "./docker/myapp",
    },

    // Start immediately after creation
    Started: true,

    // Override entrypoint
    Entrypoint: []string{"/entrypoint.sh"},

    // Command to run
    Cmd: []string{"sleep", "30"},

    // Environment variables
    Env: map[string]string{"DEBUG": "true"},

    // Exposed ports
    ExposedPorts: []string{"8080/tcp"},

    // Files to copy into the container
    Files: []testcontainers.ContainerFile{...},

    // Tmpfs mounts
    Tmpfs: map[string]string{"/tmp": ""},

    // Wait strategy for readiness
    WaitingFor: wait.ForHTTP("/health"),

    // Advanced: modify container config
    ConfigModifier: func(c *container.Config) { ... },

    // Advanced: modify host config (mounts, privileged, etc.)
    HostConfigModifier: func(hc *container.HostConfig) { ... },

    // Optional: callback when container is destroyed
    OnDestroy: func(c testworld.WorldContainer) {
        // Collect log files from the container
        c.LogFile("/var/log/app.log")
    },
}
```

### WorldContainer

```go
// Create a container (set Started: true in spec, or call Start() manually)
wc := w.NewContainer(spec)

// Start the container (if not auto-started)
wc.Start()

// Execute a command (fails test if exit code doesn't match)
wc.Exec([]string{"echo", "hello"}, 0)

// Wait for a condition
wc.Wait(wait.ForLog("Server started"))

// Copy a file from container to the world log
wc.LogFile("/var/log/app.log")
```

### Network Connectivity

Containers can reach each other by name:

```go
spec := testworld.ContainerSpec{
    Image:   "alpine:latest",
    Cmd:     []string{"sleep", "60"},
    Started: true,
}

server := w.NewContainer(spec)
client := w.NewContainer(spec)

// Client can ping server by container name
client.Exec([]string{"ping", "-c", "1", server.Name}, 0)
```

## World Log

When a log path is provided, the World creates:

- An ASCII Gantt chart showing event timelines
- A combined log file with all container outputs

Example output:
```
Event Timeline (Total: 2.500s):
ID  | Process Visualization
----|--------------------------------------------------------------------------------
000 |[####] (0.500s) World: Create
001 |     [##] (0.200s) World: add orca container
002 |       [########] (0.800s) TestExample-orca-1: wait
003 |               [#] (0.100s) TestExample-orca-1: exec varnish-supervisor --version
```

## License

MIT
