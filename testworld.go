package testworld

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	timelineWidth = 80
)

// basename returns the last component of a path, stripping any suffix after ":".
// e.g., "alpine:latest" -> "alpine", "docker.io/library/nginx:1.19" -> "nginx"
func basename(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// world represents the environment in which a test runs. Containers added to
// the world share the same network and logs are collected in a common log file.
// The world is destroyed at the end of the test.
type World struct {
	name           string
	ctx            context.Context
	t              *testing.T
	worldLog       *WorldLog
	cn             *testcontainers.DockerNetwork
	containers     map[string]WorldContainer
	containerKinds map[string]int
}

type WorldContainer struct {
	world     *World
	Name      string
	container testcontainers.Container
	onDestroy func(WorldContainer)
}

// New creates a new testworld. w.Destroy() should be deferred right after
// calling this function. If logPath is not empty, a world log will be
// created in the specified directory.
func New(t *testing.T, logPath string) *World {
	var w World

	// All tests in this package run in isolated worlds, so they should be
	// able to run in parallel.
	t.Parallel()

	// Skip the testworld tests if running in short mode.
	if testing.Short() {
		t.Skip("skipping testworld test in short mode")
	}

	w.t = t
	w.name = t.Name()
	w.ctx = context.Background()
	w.containers = make(map[string]WorldContainer)
	w.containerKinds = make(map[string]int)

	// Creating a world log is optional. If logPath is empty, we use a
	// dummy world log that does nothing.
	if logPath != "" {
		// Use test tmpdir for intermediate logs
		logDir := fmt.Sprintf("%s/worldlogs", t.TempDir())
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Log("Failed to create world log directory:", err)
		}

		// Create the world log.
		worldLog, err := NewWorldLog(&w, logPath)
		if err != nil {
			t.Log("Failed to create world log:", err)
			w.worldLog = &WorldLog{}
		} else {
			w.worldLog = worldLog
		}
	} else {
		// No world log specified, use a dummy.
		w.worldLog = &WorldLog{}
	}

	event := w.worldLog.newEvent("World: Create")
	defer event.finish()

	// Create a container network for the test.
	cn, err := network.New(w.ctx,
		network.WithDriver("bridge"),
		network.WithAttachable(),
	)
	testcontainers.CleanupNetwork(t, cn)
	if err != nil {
		w.Destroy()
		t.Fatalf("Failed to create network: %v", err)
	}
	w.cn = cn

	return &w
}

// Destroy cleans up the testworld.
func (w *World) Destroy() {
	if w == nil {
		return
	}

	event := w.worldLog.newEvent("World: destroy")

	for name, c := range w.containers {
		// Try to collect logs, but don't fail if it doesn't work during cleanup
		if err := c.logInternal(); err != nil {
			w.t.Log("Failed to collect logs for container ", name, ": ", err)
		}

		if c.onDestroy != nil {
			c.onDestroy(c)
		}

		// Terminate the container with a half a second timeout
		// The default timeout is 10 seconds, after which Docker sends SIGKILL
		err := c.container.Terminate(w.ctx, testcontainers.StopTimeout(time.Millisecond*500))
		if err != nil {
			w.t.Log("Failed to terminate container ", name, ": ", err)
		}
	}

	event.finish()
	w.worldLog.finish()
}

// logInternal writes the container logs to the world log.
func (wc *WorldContainer) logInternal() error {
	event := wc.world.worldLog.newEvent("%s: logs", wc.Name)
	defer event.finish()

	logsReader, err := wc.container.Logs(wc.world.ctx)
	if err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
	}
	defer logsReader.Close()

	if event != nil {
		if _, err = io.Copy(event.log, logsReader); err != nil {
			return fmt.Errorf("failed to copy logs: %w", err)
		}
	}
	return nil
}

// NewContainer creates a new TestContainer and adds it to the World.
// Set spec.Started to true to start the container immediately, or call Start() later.
func (w *World) NewContainer(spec ContainerSpec) WorldContainer {
	// Derive kind from image name, or dockerfile context as fallback
	kind := basename(spec.Image)
	if kind == "" {
		kind = basename(spec.FromDockerfile.Context)
	}
	if kind == "" {
		kind = "container"
	}

	event := w.worldLog.newEvent("World: add %s container", kind)
	defer event.finish()

	// Generate a unique name for the container
	w.containerKinds[kind]++
	name := fmt.Sprintf("%s-%s-%d", w.name, kind, w.containerKinds[kind])

	// Convert spec to testcontainers request
	containerRequest := spec.toGenericContainerRequest(name, w.cn.Name)

	// Create the container
	container, err := testcontainers.GenericContainer(w.ctx, containerRequest)
	testcontainers.CleanupContainer(w.t, container)
	if err != nil {
		w.t.Fatalf("Failed to create container %s: %v", name, err)
	}

	wc := WorldContainer{
		world:     w,
		Name:      name,
		container: container,
		onDestroy: spec.OnDestroy,
	}

	// Add the container to the world
	w.containers[name] = wc
	return wc
}

// Start starts the container.
func (wc *WorldContainer) Start() {
	event := wc.world.worldLog.newEvent("%s: start", wc.Name)
	defer event.finish()

	if err := wc.container.Start(wc.world.ctx); err != nil {
		wc.world.t.Fatalf("Failed to start container %s: %v", wc.Name, err)
	}
}

// Exec executes a command in a container and writes the output to the world log.
func (wc *WorldContainer) Exec(cmd []string, expectCode int) {
	event := wc.world.worldLog.newEvent("%s: exec %s", wc.Name, strings.Join(cmd, " "))
	defer event.finish()

	exitCode, logsReader, err := wc.container.Exec(wc.world.ctx, cmd)
	if err != nil {
		wc.world.t.Fatalf("Failed to exec in container %s: %v", wc.Name, err)
	}
	// Write the logs to file, demultiplexing stdout and stderr
	if event != nil {
		defer stdcopy.StdCopy(event.log, event.log, logsReader)
	}

	if exitCode != expectCode {
		wc.world.t.Fatalf("Command %v exited with code %d (expected %d)", cmd, exitCode, expectCode)
	}
}

// Wait waits for a container with a given wait strategy.
func (wc *WorldContainer) Wait(waitStrategy wait.Strategy) {
	event := wc.world.worldLog.newEvent("%s: wait", wc.Name)
	defer event.finish()

	if err := waitStrategy.WaitUntilReady(wc.world.ctx, wc.container); err != nil {
		wc.world.t.Fatalf("Wait failed for container %s: %v", wc.Name, err)
	}
}

// LogFile is used to log large files from a container to the world log.
func (wc *WorldContainer) LogFile(path string) {
	event := wc.world.worldLog.newEvent("%s: log file %s", wc.Name, path)
	defer event.finish()

	reader, err := wc.container.CopyFileFromContainer(wc.world.ctx, path)
	if err != nil {
		wc.world.t.Fatalf("Failed to copy file %s from container %s: %v", path, wc.Name, err)
	}
	defer reader.Close()

	if event != nil {
		if _, err = io.Copy(event.log, reader); err != nil {
			wc.world.t.Fatalf("Failed to copy file content for %s: %v", path, err)
		}
	}
}
