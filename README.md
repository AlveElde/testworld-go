# testworld-go

`testworld-go` is a system test framework based on `testcontainers-go`. Testcontainers is very flexible, but needs a lot of boilerplate for every test. Testworld cuts down on the boilerplate with an opinionated approach.

## Features

- **Async**: Containers are created asynchronously, leading to faster tests when more than one container is used.
- **Test isolation**: Each test creates a separate namespace and docker bridge network.
- **Replicas**: Create and control groups of identical containers.
- **Low boilerplate**: Reduced boilerplate compared to `testcontainers-go`
- **Log collection**: Collect logs from all containers and output to a verbose log file. 
- **Event tracking**: Outputs a timeline of events during the test.

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

func TestWebCluster(t *testing.T) {
    w := testworld.New(t, "")
    defer w.Destroy()

    // Spin up 3 web servers and a client — all 4 containers are created in parallel
    servers := w.NewContainer(testworld.ContainerSpec{
        Image:      "caddy:latest",
        Replicas:   3,
        WaitingFor: wait.ForHTTP("/").WithPort("80/tcp"),
    })

    client := w.NewContainer(testworld.ContainerSpec{
        Image: "alpine:latest",
        Cmd:   []string{"sleep", "60"},
    })

    // Wait for all servers to be ready
    servers.Await()

    // The group name resolves to all 3 server IPs via DNS round-robin
    client.Exec([]string{"wget", "-q", "-O", "/dev/null", "http://" + servers.Name}, 0)
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

    // Create multiple identical containers as a group (default: 1)
    Replicas: 3,

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

Containers are created asynchronously. All methods on `WorldContainer`
transparently wait for the container to be ready before proceeding:

```go
// These return immediately — both containers are created in parallel
db := w.NewContainer(dbSpec)
app := w.NewContainer(appSpec)

// First method call on each container blocks until it is ready
db.Wait(wait.ForLog("database system is ready to accept connections"))
app.Wait(wait.ForHTTP("/healthz").WithPort("8080/tcp"))

// Execute a command (fails test if exit code doesn't match)
app.Exec([]string{"curl", "-sf", "http://localhost:8080/healthz"}, 0)

// Block until ready without performing any action
app.Await()

// Copy a file from container to the world log
app.LogFile("/var/log/app.log")
```

### Replicas

Set `Replicas` to create a group of identical containers. All methods on the
`WorldContainer` execute across every replica. The group name resolves to all
replica IPs via Docker DNS round-robin:

```go
servers := w.NewContainer(testworld.ContainerSpec{
    Image:      "caddy:latest",
    Replicas:   3,
    WaitingFor: wait.ForHTTP("/").WithPort("80/tcp"),
})

client := w.NewContainer(testworld.ContainerSpec{
    Image: "alpine:latest",
    Cmd:   []string{"sleep", "60"},
})

// Wait for all servers to be ready
servers.Await()

// The group name resolves to all 3 server IPs
client.Exec([]string{"wget", "-q", "-O", "/dev/null", "http://" + servers.Name}, 0)
```

Each replica also gets its own unique name (`servers.Name + "-1"`, `-2`, etc.)
for individual addressing.

## World Log

When a log path is provided, the World creates:

- An ASCII Gantt chart showing event timelines
- A combined log file with all container outputs

Example output:
```
Event Timeline (Total: 3.200s):
ID  | Process Visualization
----|--------------------------------------------------------------------------------
000 |[###] (0.500s) World: Create
001 |    [#####] (0.700s) World: add caddy container TestWebCluster-caddy-1-1
002 |    [######] (0.800s) World: add caddy container TestWebCluster-caddy-1-2
003 |    [######] (0.800s) World: add caddy container TestWebCluster-caddy-1-3
004 |    [####] (0.600s) World: add alpine container TestWebCluster-alpine-1
005 |              [#] (0.100s) TestWebCluster-alpine-1: exec wget -q -O /dev/null ...
```

## License

MIT
