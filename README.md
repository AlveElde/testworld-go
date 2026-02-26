# testworld-go

`testworld-go` is a system test framework based on [testcontainers-go](https://github.com/testcontainers/testcontainers-go). Testcontainers is very flexible, but needs a lot of boilerplate for every test. Testworld cuts down on the boilerplate with an opinionated approach.

## Features

- **Async**: Containers are created asynchronously, leading to faster tests when more than one container is used.
- **Test isolation**: Each test creates a separate namespace and docker bridge network.
- **Replicas**: Create and control groups of identical containers.
- **Network isolation**: Optionally block a container's internet access while keeping intra-world communication intact.
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
        Image:     "alpine:latest",
        KeepAlive: true,
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

    // Keep the container running indefinitely (uses "sleep infinity" when no Cmd is set)
    KeepAlive: true,

    // Override entrypoint
    Entrypoint: []string{"/entrypoint.sh"},

    // Command to run (overrides KeepAlive)
    Cmd: []string{"myapp", "--config", "/etc/myapp.conf"},

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

    // Extra DNS aliases 
    Aliases: []string{"db", "primary"},

    // Block internet access (see Network Isolation below)
    Isolated: true,

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
    Image:     "alpine:latest",
    KeepAlive: true,
})

// Wait for all servers to be ready
servers.Await()

// The group name resolves to all 3 server IPs
client.Exec([]string{"wget", "-q", "-O", "/dev/null", "http://" + servers.Name}, 0)
```

Each replica also gets its own unique name (`servers.Name + "-1"`, `-2`, etc.)
for individual addressing.

## Network Isolation

Set `Isolated: true` on a `ContainerSpec` to block that container's access to
the internet while keeping intra-world communication intact.

Internally, testworld maintains two Docker bridge networks:

| Network | Internet | Intra-world |
|---------|----------|-------------|
| External (regular bridge) | yes | yes |
| Internal (`--internal` bridge) | no | yes |

Every container joins the internal network. Non-isolated containers also join
the external network, gaining internet access via its gateway. Isolated
containers join only the internal network — Docker omits the default gateway
for `--internal` networks, so any attempt to reach an external address fails
immediately with "Network unreachable".

```go
// A mock server that should never call out to the real internet
mock := w.NewContainer(testworld.ContainerSpec{
    Image:    "my-mock-server:latest",
    Isolated: true,
})

// A regular client that can reach both the internet and the mock server
client := w.NewContainer(testworld.ContainerSpec{
    Image:     "alpine:latest",
    KeepAlive: true,
})

// The client can reach the mock server by name
client.Exec([]string{"wget", "-q", "-O", "/dev/null", "http://" + mock.Name + ":8080/"}, 0)

// The mock server cannot reach the internet
mock.Exec([]string{"ping", "-c", "1", "-W", "2", "8.8.8.8"}, 1)
```

## World Log

When a log path is provided, the World creates:

- An ASCII Gantt chart showing event timelines
- A combined log file with all container outputs

Example output:
```
Event Timeline (Total: 3.284s):
ID  | Process Visualization
----|--------------------------------------------------------------------------------
000 |[################] (0.672s) World: Create
001 |                [#############################] (1.199s) World: add caddy container TestReplicaHTTPClients-caddy-1-1
002 |                [#############################] (1.210s) TestReplicaHTTPClients-caddy-1: await
003 |                [############################] (1.173s) World: add caddy container TestReplicaHTTPClients-caddy-1-2
004 |                [###########################] (1.121s) World: add curl container TestReplicaHTTPClients-curl-1-1
005 |                [#############################] (1.210s) World: add caddy container TestReplicaHTTPClients-caddy-1-3
006 |                [######################] (0.941s) World: add curl container TestReplicaHTTPClients-curl-1-2
007 |                [###################] (0.804s) World: add curl container TestReplicaHTTPClients-curl-1-3
008 |                                             [##] (0.102s) TestReplicaHTTPClients-curl-1-3: exec curl http://TestReplicaHTTPClients-caddy-1:80/
009 |                                             [##] (0.102s) TestReplicaHTTPClients-curl-1-1: exec curl http://TestReplicaHTTPClients-caddy-1:80/
010 |                                             [##] (0.102s) TestReplicaHTTPClients-curl-1-2: exec curl http://TestReplicaHTTPClients-caddy-1:80/
011 |                                                [#] (0.000s) TestReplicaHTTPClients-caddy-1: await
012 |                                                [#] (0.000s) TestReplicaHTTPClients-curl-1: await
013 |                                                [###############################] (1.300s) World: destroy
014 |                                                [#] (0.003s) TestReplicaHTTPClients-curl-1-3: logs
015 |                                                [#] (0.003s) TestReplicaHTTPClients-curl-1-1: logs
016 |                                                [#] (0.005s) TestReplicaHTTPClients-caddy-1-3: logs
017 |                                                [#] (0.005s) TestReplicaHTTPClients-curl-1-2: logs
018 |                                                [#] (0.006s) TestReplicaHTTPClients-caddy-1-1: logs
019 |                                                [#] (0.005s) TestReplicaHTTPClients-caddy-1-2: logs
```

## License

MIT
