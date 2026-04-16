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

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	timelineWidth = 80
)

// sharedNetworks holds a single pair of Docker networks shared by all World
// instances in this test binary. Networks are created on first use and removed
// when the last World is destroyed.
type sharedNetworks struct {
	mu   sync.Mutex
	cn   *testcontainers.DockerNetwork // external bridge
	icn  *testcontainers.DockerNetwork // internal bridge
	refs int
}

var shared sharedNetworks

// acquire increments the reference count and returns the shared networks,
// creating them first if no World currently holds a reference.
// Every acquire must be paired with exactly one release.
func (s *sharedNetworks) acquire(ctx context.Context, t *testing.T) (external, internal *testcontainers.DockerNetwork) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.refs == 0 {
		// No live World holds the networks; create a fresh pair.
		type result struct {
			net *testcontainers.DockerNetwork
			err error
		}
		extCh := make(chan result, 1)
		intCh := make(chan result, 1)

		go func() {
			n, err := network.New(ctx, network.WithDriver("bridge"), network.WithAttachable())
			extCh <- result{n, err}
		}()
		go func() {
			n, err := network.New(ctx, network.WithDriver("bridge"), network.WithAttachable(), network.WithInternal())
			intCh <- result{n, err}
		}()

		ext, int_ := <-extCh, <-intCh
		if ext.err != nil {
			t.Fatalf("Failed to create external network: %v", ext.err)
		}
		if int_.err != nil {
			t.Fatalf("Failed to create internal network: %v", int_.err)
		}
		s.cn = ext.net
		s.icn = int_.net
	}

	s.refs++
	return s.cn, s.icn
}

// release decrements the reference count and removes the shared networks
// when it reaches zero.
func (s *sharedNetworks) release(ctx context.Context, t *testing.T) {
	s.mu.Lock()
	s.refs--
	if s.refs > 0 {
		s.mu.Unlock()
		return
	}
	// Capture and clear the pointers under the lock so a concurrent acquire
	// sees refs == 0 and cn == nil, triggering fresh network creation.
	cn, icn := s.cn, s.icn
	s.cn, s.icn = nil, nil
	s.mu.Unlock()

	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Log("Failed to create Docker client for network cleanup: ", err)
		return
	}
	defer docker.Close()
	if cn != nil {
		//nolint:errcheck
		docker.NetworkRemove(ctx, cn.Name)
	}
	if icn != nil {
		//nolint:errcheck
		docker.NetworkRemove(ctx, icn.Name)
	}
}

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
	aliases   []string // DNS aliases registered on the shared networks
	ready     chan struct{}
	container testcontainers.Container
	err       error
}

type WorldContainer struct {
	world     *World
	Name      string
	image     string // image name, or "dockerfile:<context>" for custom builds
	isolated  bool
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
	w.name = strings.ReplaceAll(t.Name(), "/", "-")
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

	// Acquire shared networks (created once, reused across all parallel tests).
	w.cn, w.icn = shared.acquire(w.ctx, t)

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

	// Collect logs from all containers concurrently.
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
					if err := c.logOneInternal(pc.name, pc.container); err != nil {
						w.t.Log("Failed to collect logs for container ", pc.name, ": ", err)
					}
				}(pc)
			}
			pwg.Wait()
		}(c)
	}
	wg.Wait()

	// Force-remove all containers concurrently using the Docker client
	// directly. This skips testcontainers' Stop (SIGTERM → wait → SIGKILL)
	// and lifecycle hooks, issuing a single SIGKILL+remove per container.
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		w.t.Log("Failed to create Docker client for cleanup: ", err)
	} else {
		defer docker.Close()
		var rmWg sync.WaitGroup
		for _, c := range w.containers {
			for _, pc := range c.pending {
				if pc.err != nil {
					continue
				}
				rmWg.Add(1)
				go func(id string) {
					defer rmWg.Done()
					//nolint:errcheck
					docker.ContainerRemove(w.ctx, id, container.RemoveOptions{
						RemoveVolumes: true,
						Force:         true,
					})
				}(pc.container.GetContainerID())
			}
		}
		rmWg.Wait()
	}

	event.finish()
	w.worldLog.finish()

	// Release our reference to the shared networks.
	// The last World to release removes them.
	if w.cn != nil {
		shared.release(w.ctx, w.t)
	}
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

	// Derive a human-readable image label for the inventory log.
	imageLabel := spec.Image
	if imageLabel == "" {
		ctx := spec.FromDockerfile.Context
		if ctx == "" {
			ctx = "<archive>"
		}
		imageLabel = "dockerfile:" + ctx
	}

	wc := WorldContainer{
		world:     w,
		Name:      name,
		image:     imageLabel,
		isolated:  spec.Isolated,
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
			name:    replicaName,
			aliases: aliases,
			ready:   make(chan struct{}),
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
				// Place the CA in the OS trust store directory so
				// update-ca-certificates can pick it up.
				testcontainers.ContainerFile{Reader: bytes.NewReader(w.tls.certPEM), ContainerFilePath: "/usr/local/share/ca-certificates/testworld-ca.crt", FileMode: 0o644},
				// Mount the pre-built combined CA bundle directly into
				// the well-known trust store paths, avoiding per-container
				// Docker API calls to read-modify-write the trust store.
				testcontainers.ContainerFile{Reader: bytes.NewReader(w.tls.bundlePEM), ContainerFilePath: "/etc/ssl/certs/ca-certificates.crt", FileMode: 0o644},
				testcontainers.ContainerFile{Reader: bytes.NewReader(w.tls.bundlePEM), ContainerFilePath: "/etc/pki/tls/certs/ca-bundle.crt", FileMode: 0o644},
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
		exitCode, logsReader, err := pc.container.Exec(wc.world.ctx, cmd, tcexec.Multiplexed())
		if err != nil {
			wc.world.t.Errorf("Failed to exec in container %s: %v", pc.name, err)
			return false
		}
		if logsReader != nil {
			if event != nil {
				io.Copy(event.log, logsReader)
			} else {
				io.Copy(io.Discard, logsReader)
			}
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
