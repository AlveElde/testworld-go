package testworld

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	cn             *testcontainers.DockerNetwork // external: bridge with internet access
	icn            *testcontainers.DockerNetwork // internal: no internet, shared by all containers
	containers     map[string]WorldContainer
	containerKinds map[string]int
	tls            *worldCA
}

// pendingContainer holds the result of an async container creation.
// The goroutine writes container/err before closing ready, ensuring
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
	after     []WorldContainer
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

	// Create the external network (regular bridge with internet access).
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

	// Create the internal network (no internet access). All containers join
	// this network so they can communicate with each other regardless of
	// isolation. Isolated containers join only this network.
	icn, err := network.New(w.ctx,
		network.WithDriver("bridge"),
		network.WithAttachable(),
		network.WithInternal(),
	)
	testcontainers.CleanupNetwork(t, icn)
	if err != nil {
		w.Destroy()
		t.Fatalf("Failed to create internal network: %v", err)
	}
	w.icn = icn

	// Generate a World-scoped CA so every container gets a TLS certificate.
	// Certificates are mounted at TLSCACertPath, TLSCertPath, and TLSKeyPath.
	ca, err := newWorldCA()
	if err != nil {
		w.Destroy()
		t.Fatalf("Failed to create TLS CA: %v", err)
	}
	w.tls = ca

	return &w
}

// AwaitAll waits for all containers in the world to be ready.
func (w *World) AwaitAll() {
	for _, c := range w.containers {
		c.Await()
	}
}

// Destroy cleans up the testworld.
func (w *World) Destroy() {
	if w == nil {
		return
	}

	// Wait for all containers to be ready before starting the teardown.
	w.AwaitAll()

	event := w.worldLog.newEvent("World: destroy")

	// Destroy all containers concurrently.
	var wg sync.WaitGroup
	for _, c := range w.containers {
		wg.Add(1)
		go func(c WorldContainer) {
			defer wg.Done()

			if c.onDestroy != nil {
				c.onDestroy(c)
			}

			var pwg sync.WaitGroup
			for _, pc := range c.pending {
				pwg.Add(1)
				go func(pc *pendingContainer) {
					defer pwg.Done()
					if pc.err != nil {
						w.t.Log("Container ", pc.name, " failed to create: ", pc.err)
						return
					}

					// Try to collect logs, but don't fail if it doesn't work during cleanup
					if err := c.logOneInternal(pc.name, pc.container); err != nil {
						w.t.Log("Failed to collect logs for container ", pc.name, ": ", err)
					}

					// Terminate the container with a half a second timeout
					// The default timeout is 10 seconds, after which Docker sends SIGKILL
					if err := pc.container.Terminate(w.ctx, testcontainers.StopTimeout(time.Millisecond*500)); err != nil {
						w.t.Log("Failed to terminate container ", pc.name, ": ", err)
					}
				}(pc)
			}
			pwg.Wait()
		}(c)
	}
	wg.Wait()

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
		kind = spec.FromDockerfile.Repo
	}
	if kind == "" {
		kind = basename(spec.FromDockerfile.Context)
	}
	if kind == "" {
		kind = "container"
	}

	// Generate a unique group name for the container
	w.containerKinds[kind]++
	name := strings.ToLower(fmt.Sprintf("%s-%s-%d", w.name, kind, w.containerKinds[kind]))

	replicas := max(spec.Replicas, 1)
	pending := make([]*pendingContainer, replicas)

	wc := WorldContainer{
		world:     w,
		Name:      name,
		pending:   pending,
		after:     spec.After,
		onDestroy: spec.OnDestroy,
	}

	// Add the container to the world synchronously so Destroy() can find it
	w.containers[name] = wc

	// Buffer any io.Reader-based file contents once before spawning replica
	// goroutines. An io.Reader can only be consumed once, so each replica must
	// get its own independent bytes.Reader over the same underlying bytes.
	fileContents := make([][]byte, len(spec.Files))
	for i, f := range spec.Files {
		if f.Reader != nil {
			data, err := io.ReadAll(f.Reader)
			if err != nil {
				w.t.Fatalf("Failed to buffer file %q for container %s: %v", f.ContainerFilePath, name, err)
			}
			fileContents[i] = data
		}
	}

	// Buffer ContextArchive (io.ReadSeeker) for the same reason: the first
	// replica goroutine to run would exhaust the reader, leaving every other
	// replica with an empty archive.
	var contextArchiveData []byte
	if spec.FromDockerfile.ContextArchive != nil {
		data, err := io.ReadAll(spec.FromDockerfile.ContextArchive)
		if err != nil {
			w.t.Fatalf("Failed to buffer ContextArchive for container %s: %v", name, err)
		}
		contextArchiveData = data
	}

	for i := range replicas {
		// For a single replica, the replica name is the group name.
		// For multiple replicas, each gets a unique suffix.
		replicaName := name
		aliases := []string{name}
		if replicas > 1 {
			replicaName = fmt.Sprintf("%s-%d", name, i+1)
			aliases = []string{replicaName, name}
		}
		aliases = append(aliases, spec.Aliases...)

		// Expand subdomains: join each subdomain with each existing alias.
		if len(spec.Subdomains) > 0 {
			base := make([]string, len(aliases))
			copy(base, aliases)
			for _, sub := range spec.Subdomains {
				for _, a := range base {
					aliases = append(aliases, sub+"."+a)
				}
			}
		}

		pc := &pendingContainer{
			name:  replicaName,
			ready: make(chan struct{}),
		}
		pending[i] = pc

		containerRequest := spec.toGenericContainerRequest(replicaName, w.cn.Name, w.icn.Name, aliases)

		// Give this replica its own readers so goroutines don't race over
		// shared io.Reader state. HostFilePath-based files are unaffected.
		replicaFiles := make([]testcontainers.ContainerFile, len(spec.Files))
		copy(replicaFiles, spec.Files)
		for j, data := range fileContents {
			if data != nil {
				replicaFiles[j].Reader = bytes.NewReader(data)
			}
		}
		containerRequest.ContainerRequest.Files = replicaFiles

		// If TLS is enabled, generate a certificate for this replica and
		// mount the CA cert, leaf cert, and key into the container.
		if w.tls != nil {
			certPEM, keyPEM, err := w.tls.generateCert(aliases)
			if err != nil {
				w.t.Fatalf("Failed to generate TLS cert for %s: %v", replicaName, err)
			}
			containerRequest.ContainerRequest.Files = append(containerRequest.ContainerRequest.Files,
				testcontainers.ContainerFile{Reader: bytes.NewReader(w.tls.certPEM), ContainerFilePath: TLSCACertPath, FileMode: 0o644},
				testcontainers.ContainerFile{Reader: bytes.NewReader(certPEM), ContainerFilePath: TLSCertPath, FileMode: 0o644},
				testcontainers.ContainerFile{Reader: bytes.NewReader(keyPEM), ContainerFilePath: TLSKeyPath, FileMode: 0o644},
				// Also place the CA in the OS trust store directory so
				// update-ca-certificates can pick it up.
				testcontainers.ContainerFile{Reader: bytes.NewReader(w.tls.certPEM), ContainerFilePath: "/usr/local/share/ca-certificates/testworld-ca.crt", FileMode: 0o644},
			)

			// Install the CA into the system trust store so that TLS
			// clients (curl, wget, Go, etc.) trust it automatically.
			// This runs after container creation but before start, so
			// the CA is present before the application reads the store.
			caPEM := w.tls.certPEM
			containerRequest.ContainerRequest.LifecycleHooks = append(
				containerRequest.ContainerRequest.LifecycleHooks,
				testcontainers.ContainerLifecycleHooks{
					PostCreates: []testcontainers.ContainerHook{
						func(ctx context.Context, c testcontainers.Container) error {
							for _, p := range []string{
								"/etc/ssl/certs/ca-certificates.crt",
								"/etc/pki/tls/certs/ca-bundle.crt",
							} {
								reader, err := c.CopyFileFromContainer(ctx, p)
								if err != nil {
									continue
								}
								bundle, _ := io.ReadAll(reader)
								reader.Close()
								if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
									bundle = append(bundle, '\n')
								}
								bundle = append(bundle, caPEM...)
								//nolint:errcheck
								c.CopyToContainer(ctx, bundle, p, 0o644)
							}
							return nil
						},
					},
				},
			)

			env := make(map[string]string, len(containerRequest.ContainerRequest.Env)+3)
			for k, v := range containerRequest.ContainerRequest.Env {
				env[k] = v
			}
			env["TLS_CA_CERT"] = TLSCACertPath
			env["TLS_CERT"] = TLSCertPath
			env["TLS_KEY"] = TLSKeyPath
			containerRequest.ContainerRequest.Env = env
		}

		if contextArchiveData != nil {
			containerRequest.ContainerRequest.FromDockerfile.ContextArchive = bytes.NewReader(contextArchiveData)
		}

		// createFn performs the actual container creation. Event tracking lives
		// here so the Gantt chart reflects actual creation time.
		createFn := func() {
			// Wait for dependencies to be ready before creating this container.
			for _, dep := range spec.Requires {
				for _, dpc := range dep.pending {
					<-dpc.ready
					if dpc.err != nil {
						pc.err = fmt.Errorf("dependency %s failed: %w", dep.Name, dpc.err)
						close(pc.ready)
						return
					}
				}
			}

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
	if wc.isReady {
		return
	}
	event := wc.world.worldLog.newEvent("%s: await", wc.Name)
	defer event.finish()
	wc.forEachReady(func(_ *pendingContainer) bool { return true })
	wc.isReady = true
}

// forEachReady runs fn concurrently for each replica, waiting for its creation
// goroutine to finish before calling fn. If any container failed to create, or
// if fn returns false, the test is failed with FailNow after all goroutines finish.
func (wc *WorldContainer) forEachReady(fn func(pc *pendingContainer) bool) {
	// Wait for After dependencies before proceeding.
	for _, dep := range wc.after {
		for _, dpc := range dep.pending {
			<-dpc.ready
			if dpc.err != nil {
				wc.world.t.Fatalf("After dependency %s failed: %v", dep.Name, dpc.err)
			}
		}
	}

	var wg sync.WaitGroup
	var failed atomic.Bool
	for _, pc := range wc.pending {
		wg.Add(1)
		go func(pc *pendingContainer) {
			defer wg.Done()
			<-pc.ready
			if pc.err != nil {
				wc.world.t.Errorf("Container %s failed to create: %v", pc.name, pc.err)
				failed.Store(true)
				return
			}
			if !fn(pc) {
				failed.Store(true)
			}
		}(pc)
	}
	wg.Wait()
	if failed.Load() {
		wc.world.t.FailNow()
	}
}

// Exec executes a command in all replica containers concurrently.
func (wc *WorldContainer) Exec(cmd []string, expectCode int) {
	wc.forEachReady(func(pc *pendingContainer) bool {
		event := wc.world.worldLog.newEvent("%s: exec %s", pc.name, strings.Join(cmd, " "))
		defer event.finish()
		exitCode, logsReader, err := pc.container.Exec(wc.world.ctx, cmd)
		if err != nil {
			wc.world.t.Errorf("Failed to exec in container %s: %v", pc.name, err)
			return false
		}
		if event != nil {
			stdcopy.StdCopy(event.log, event.log, logsReader)
		}
		if exitCode != expectCode {
			wc.world.t.Errorf("Command %v exited with code %d (expected %d) in container %s", cmd, exitCode, expectCode, pc.name)
			return false
		}
		return true
	})
}

// Wait waits for all replica containers concurrently with a given wait strategy.
func (wc *WorldContainer) Wait(waitStrategy wait.Strategy) {
	wc.forEachReady(func(pc *pendingContainer) bool {
		event := wc.world.worldLog.newEvent("%s: wait", pc.name)
		defer event.finish()
		if err := waitStrategy.WaitUntilReady(wc.world.ctx, pc.container); err != nil {
			wc.world.t.Errorf("Wait failed for container %s: %v", pc.name, err)
			return false
		}
		return true
	})
}

// LogFile copies a file from all replica containers to the world log concurrently.
func (wc *WorldContainer) LogFile(path string) {
	wc.forEachReady(func(pc *pendingContainer) bool {
		event := wc.world.worldLog.newEvent("%s: log file %s", pc.name, path)
		defer event.finish()
		reader, err := pc.container.CopyFileFromContainer(wc.world.ctx, path)
		if err != nil {
			wc.world.t.Errorf("Failed to copy file %s from container %s: %v", path, pc.name, err)
			return false
		}
		defer reader.Close()
		if event != nil {
			if _, err = io.Copy(event.log, reader); err != nil {
				wc.world.t.Errorf("Failed to copy file content for %s: %v", path, err)
				return false
			}
		}
		return true
	})
}
