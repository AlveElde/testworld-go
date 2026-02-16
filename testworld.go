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

// pendingContainer holds the result of an async container creation.
// The goroutine writes container/err before closing ready.
// Readers call waitReady() which receives from ready, ensuring
// happens-before ordering per the Go memory model.
type pendingContainer struct {
	name      string
	ready     chan struct{}
	container testcontainers.Container
	err       error
}

type WorldContainer struct {
	world     *World
	Name      string
	isReady   bool
	pending   []*pendingContainer
	onDestroy func(WorldContainer)
}

// waitReady blocks until all replica creation goroutines finish.
func (wc *WorldContainer) waitReady() ([]testcontainers.Container, error) {
	containers := make([]testcontainers.Container, len(wc.pending))
	for i, pc := range wc.pending {
		<-pc.ready
		if pc.err != nil {
			return containers, fmt.Errorf("%s: %w", pc.name, pc.err)
		}
		containers[i] = pc.container
	}
	return containers, nil
}

// mustReady blocks until all replicas are ready and fatals if any creation failed.
func (wc *WorldContainer) mustReady() []testcontainers.Container {
	if !wc.isReady {
		event := wc.world.worldLog.newEvent("%s: await", wc.Name)
		defer event.finish()
	}
	containers, err := wc.waitReady()
	if err != nil {
		wc.world.t.Fatalf("Container %s failed to create: %v", wc.Name, err)
	}
	wc.isReady = true
	return containers
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

	for _, c := range w.containers {
		for _, pc := range c.pending {
			<-pc.ready
			if pc.err != nil {
				w.t.Log("Container ", pc.name, " failed to create: ", pc.err)
				continue
			}

			// Try to collect logs, but don't fail if it doesn't work during cleanup
			if err := c.logOneInternal(pc.name, pc.container); err != nil {
				w.t.Log("Failed to collect logs for container ", pc.name, ": ", err)
			}

			// Terminate the container with a half a second timeout
			// The default timeout is 10 seconds, after which Docker sends SIGKILL
			err := pc.container.Terminate(w.ctx, testcontainers.StopTimeout(time.Millisecond*500))
			if err != nil {
				w.t.Log("Failed to terminate container ", pc.name, ": ", err)
			}
		}

		if c.onDestroy != nil {
			c.onDestroy(c)
		}
	}

	event.finish()
	w.worldLog.finish()
}

// logOneInternal writes a single container's logs to the world log.
func (wc *WorldContainer) logOneInternal(name string, container testcontainers.Container) error {
	event := wc.world.worldLog.newEvent("%s: logs", name)
	defer event.finish()

	logsReader, err := container.Logs(wc.world.ctx)
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

// NewContainer creates a new container and adds it to the World.
// Container creation happens in the background and WorldContainer methods
// wait for it to be ready. Call Await() to explicitly block until ready.
// Set spec.Replicas to create multiple identical containers as a group.
func (w *World) NewContainer(spec ContainerSpec) WorldContainer {
	// Derive kind from image name, or dockerfile context as fallback
	kind := basename(spec.Image)
	if kind == "" {
		kind = basename(spec.FromDockerfile.Context)
	}
	if kind == "" {
		kind = "container"
	}

	// Generate a unique group name for the container
	w.containerKinds[kind]++
	name := fmt.Sprintf("%s-%s-%d", w.name, kind, w.containerKinds[kind])

	replicas := max(spec.Replicas, 1)
	pending := make([]*pendingContainer, replicas)

	wc := WorldContainer{
		world:     w,
		Name:      name,
		pending:   pending,
		onDestroy: spec.OnDestroy,
	}

	// Add the container to the world synchronously so Destroy() can find it
	w.containers[name] = wc

	for i := range replicas {
		// For a single replica, the replica name is the group name.
		// For multiple replicas, each gets a unique suffix.
		replicaName := name
		aliases := []string{name}
		if replicas > 1 {
			replicaName = fmt.Sprintf("%s-%d", name, i+1)
			aliases = []string{replicaName, name}
		}

		pc := &pendingContainer{
			name:  replicaName,
			ready: make(chan struct{}),
		}
		pending[i] = pc

		containerRequest := spec.toGenericContainerRequest(replicaName, w.cn.Name, aliases)

		// createFn performs the actual container creation. Event tracking lives
		// here so the Gantt chart reflects actual creation time.
		createFn := func() {
			event := w.worldLog.newEvent("World: add %s container %s", kind, replicaName)
			defer event.finish()

			container, err := testcontainers.GenericContainer(w.ctx, containerRequest)
			testcontainers.CleanupContainer(w.t, container)

			// Write results before closing the channel (happens-before guarantee)
			pc.container = container
			pc.err = err
			close(pc.ready)
		}

		go createFn()
	}

	return wc
}

// Await blocks until all replica containers are created and started.
func (wc *WorldContainer) Await() {
	wc.mustReady()
}

// Exec executes a command in all replica containers and writes the output to the world log.
func (wc *WorldContainer) Exec(cmd []string, expectCode int) {
	containers := wc.mustReady()
	for i, container := range containers {
		name := wc.pending[i].name
		event := wc.world.worldLog.newEvent("%s: exec %s", name, strings.Join(cmd, " "))

		exitCode, logsReader, err := container.Exec(wc.world.ctx, cmd)
		if err != nil {
			wc.world.t.Fatalf("Failed to exec in container %s: %v", name, err)
		}
		// Write the logs to file, demultiplexing stdout and stderr
		if event != nil {
			stdcopy.StdCopy(event.log, event.log, logsReader)
		}

		if exitCode != expectCode {
			wc.world.t.Fatalf("Command %v exited with code %d (expected %d) in container %s", cmd, exitCode, expectCode, name)
		}

		event.finish()
	}
}

// Wait waits for all replica containers with a given wait strategy.
func (wc *WorldContainer) Wait(waitStrategy wait.Strategy) {
	containers := wc.mustReady()
	for i, container := range containers {
		name := wc.pending[i].name
		event := wc.world.worldLog.newEvent("%s: wait", name)

		if err := waitStrategy.WaitUntilReady(wc.world.ctx, container); err != nil {
			wc.world.t.Fatalf("Wait failed for container %s: %v", name, err)
		}

		event.finish()
	}
}

// LogFile copies a file from all replica containers to the world log.
func (wc *WorldContainer) LogFile(path string) {
	containers := wc.mustReady()
	for i, container := range containers {
		name := wc.pending[i].name
		event := wc.world.worldLog.newEvent("%s: log file %s", name, path)

		reader, err := container.CopyFileFromContainer(wc.world.ctx, path)
		if err != nil {
			wc.world.t.Fatalf("Failed to copy file %s from container %s: %v", path, name, err)
		}

		if event != nil {
			if _, err = io.Copy(event.log, reader); err != nil {
				reader.Close()
				wc.world.t.Fatalf("Failed to copy file content for %s: %v", path, err)
			}
		}
		reader.Close()

		event.finish()
	}
}
