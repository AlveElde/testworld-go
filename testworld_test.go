package testworld

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestWorldCreation tests that a World can be created and destroyed properly.
func TestWorldCreation(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	if w.name != t.Name() {
		t.Errorf("Expected world name %q, got %q", t.Name(), w.name)
	}

	if w.ctx == nil {
		t.Error("Expected context to be non-nil")
	}

	if w.cn == nil {
		t.Error("Expected network to be non-nil")
	}

	if w.containers == nil {
		t.Error("Expected containers map to be non-nil")
	}
}

// TestWorldWithWorldLog tests that a World can be created with a world log.
func TestWorldWithWorldLog(t *testing.T) {
	logDir := t.TempDir()
	w := New(t, logDir)
	defer w.Destroy()

	if w.worldLog == nil {
		t.Error("Expected worldLog to be non-nil")
	}
}

// TestNewContainer tests that a container can be added to the world.
func TestNewContainer(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	wc := w.NewContainer(spec)

	if wc.Name == "" {
		t.Error("Expected container name to be non-empty")
	}

	wc.Await()

	if len(wc.pending) != 1 {
		t.Errorf("Expected 1 container, got %d", len(wc.pending))
	}
	if wc.pending[0].container == nil {
		t.Error("Expected container to be non-nil")
	}

	if wc.world != w {
		t.Error("Expected container world to match")
	}

	// Check the container is in the world's container map
	if _, exists := w.containers[wc.Name]; !exists {
		t.Error("Expected container to be in world's containers map")
	}
}

// TestContainerAwait tests that Await blocks until the container is ready.
func TestContainerAwait(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	// Create container (non-blocking)
	wc := w.NewContainer(spec)

	// Explicitly wait for the container to be ready
	wc.Await()

	// Verify container is running by executing a command
	wc.Exec([]string{"echo", "hello"}, 0)
}

// TestContainerExec tests executing a command in a container.
func TestContainerExec(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	wc := w.NewContainer(spec)

	// Test successful command
	wc.Exec([]string{"echo", "hello"}, 0)

	// Test command with non-zero exit code
	wc.Exec([]string{"false"}, 1)
}

// TestContainerWait tests waiting for a container with a wait strategy.
func TestContainerWait(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	wc := w.NewContainer(spec)

	// Wait for container to be running using exec strategy
	wc.Wait(wait.ForExec([]string{"echo", "ready"}).WithStartupTimeout(5 * time.Second))
}

// TestContainerLogFile tests retrieving a file from a container.
func TestContainerLogFile(t *testing.T) {
	logDir := t.TempDir()
	w := New(t, logDir)
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sh", "-c", "echo 'file content' > /tmp/testfile.txt && sleep 30"},
	}

	wc := w.NewContainer(spec)

	// Wait for the file to be created
	wc.Wait(wait.ForExec([]string{"cat", "/tmp/testfile.txt"}).WithStartupTimeout(5 * time.Second))

	// Test LogFile method
	wc.LogFile("/tmp/testfile.txt")
}

// TestMultipleContainers tests creating multiple containers in the same world.
func TestMultipleContainers(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	// Create multiple containers
	wc1 := w.NewContainer(spec)

	wc2 := w.NewContainer(spec)

	// Verify both containers exist with unique names
	if wc1.Name == wc2.Name {
		t.Error("Expected containers to have unique names")
	}

	if len(w.containers) != 2 {
		t.Errorf("Expected 2 containers, got %d", len(w.containers))
	}

	// Verify container kind counter increments correctly
	if w.containerKinds["alpine"] != 2 {
		t.Errorf("Expected container kind count 2, got %d", w.containerKinds["alpine"])
	}
}

// TestContainerOnDestroy tests the onDestroy callback.
func TestContainerOnDestroy(t *testing.T) {
	w := New(t, "./logs")

	destroyCalled := false
	onDestroy := func(wc WorldContainer) {
		destroyCalled = true
	}

	spec := ContainerSpec{
		Image:     "alpine:latest",
		Cmd:       []string{"sleep", "30"},
		OnDestroy: onDestroy,
	}

	w.NewContainer(spec)

	// Destroy the world, which should trigger onDestroy
	w.Destroy()

	if !destroyCalled {
		t.Error("Expected onDestroy callback to be called")
	}
}

// TestDestroyAwaitsContainers verifies that Destroy waits for all container
// creation goroutines to finish before invoking onDestroy callbacks.
// It calls Destroy immediately after NewContainer (without Await) and checks
// inside onDestroy that every pending channel is already closed — meaning the
// first await-pass in Destroy completed before the cleanup-pass began.
func TestDestroyAwaitsContainers(t *testing.T) {
	w := New(t, "./logs")

	onDestroyCalled := false
	w.NewContainer(ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
		OnDestroy: func(wc WorldContainer) {
			onDestroyCalled = true
			wc.Exec([]string{"echo", "onDestroy called"}, 0)
			for _, pc := range wc.pending {
				select {
				case <-pc.ready:
					// Channel already closed: creation finished before onDestroy.
				default:
					t.Errorf("container %s not ready when onDestroy was called", pc.name)
				}
			}
		},
	})

	// Destroy immediately without calling Await — Destroy must drain all
	// pending channels before invoking the onDestroy callback.
	w.Destroy()

	if !onDestroyCalled {
		t.Error("Expected onDestroy to be called")
	}
}

// TestNetworkConnectivity tests that containers in the same world can communicate.
func TestNetworkConnectivity(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "60"},
	}

	wc1 := w.NewContainer(spec)

	wc2 := w.NewContainer(spec)

	// Test that container 1 can ping container 2 by name
	wc1.Exec([]string{"ping", "-c", "1", wc2.Name}, 0)
}

// TestIsolatedContainerCommunicatesWithTestworld tests that an isolated
// container can communicate with other containers in the same testworld
// (both isolated→regular and regular→isolated directions).
func TestIsolatedContainerCommunicatesWithTestworld(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	regular := w.NewContainer(ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "60"},
	})

	isolated := w.NewContainer(ContainerSpec{
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "60"},
		Isolated: true,
	})

	// Isolated container can reach the regular container by name.
	isolated.Exec([]string{"ping", "-c", "1", regular.Name}, 0)

	// Regular container can reach the isolated container by name.
	regular.Exec([]string{"ping", "-c", "1", isolated.Name}, 0)
}

// TestIsolatedContainerNoInternet tests that an isolated container cannot
// reach the internet (no default gateway on the internal network).
func TestIsolatedContainerNoInternet(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	isolated := w.NewContainer(ContainerSpec{
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "60"},
		Isolated: true,
	})

	// 8.8.8.8 is normally reachable from a Docker container. On an internal
	// network there is no default gateway, so ping exits immediately with
	// "Network unreachable" (exit code 1).
	isolated.Exec([]string{"ping", "-c", "1", "-W", "2", "8.8.8.8"}, 1)
}

// TestReplicaFileReaders verifies that each replica receives the complete
// contents of every io.Reader-based ContainerFile. Without the buffering fix,
// the first replica goroutine to start would exhaust the shared reader,
// leaving every other replica with an empty file.
func TestReplicaFileReaders(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	const fileContent = "hello from testworld"

	replicas := w.NewContainer(ContainerSpec{
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "60"},
		Replicas: 3,
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(fileContent),
				ContainerFilePath: "/tmp/testfile.txt",
				FileMode:          0o644,
			},
		},
	})

	// All three replicas must contain the full file content. An empty file
	// would cause grep to exit 1, failing the test.
	replicas.Exec([]string{"grep", "-qF", fileContent, "/tmp/testfile.txt"}, 0)
}

// TestReplicas tests that creating a container with Replicas > 1
// creates the correct number of replicas with unique names.
func TestReplicas(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "30"},
		Replicas: 3,
	}

	wc := w.NewContainer(spec)

	// The WorldContainer should have 3 pending replicas
	if len(wc.pending) != 3 {
		t.Fatalf("Expected 3 pending replicas, got %d", len(wc.pending))
	}

	// Wait for all replicas to be created
	wc.Await()

	// Verify each replica has a unique name with the right suffix
	for i, pc := range wc.pending {
		expectedName := fmt.Sprintf("%s-%d", wc.Name, i+1)
		if pc.name != expectedName {
			t.Errorf("Replica %d: expected name %q, got %q", i+1, expectedName, pc.name)
		}
	}

	// The world should have one entry (the group)
	if len(w.containers) != 1 {
		t.Errorf("Expected 1 container group in world, got %d", len(w.containers))
	}
}

// TestReplicaExec tests that Exec runs on all replicas.
func TestReplicaExec(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "30"},
		Replicas: 2,
	}

	wc := w.NewContainer(spec)

	// Exec should succeed on both replicas
	wc.Exec([]string{"echo", "hello"}, 0)
}

// TestReplicaDNS tests that all replicas share the group name as a DNS alias,
// so the group name resolves to all replica IPs.
func TestReplicaDNS(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	replicaSpec := ContainerSpec{
		Image:    "alpine:latest",
		Cmd:      []string{"sleep", "60"},
		Replicas: 3,
	}

	clientSpec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "60"},
	}

	servers := w.NewContainer(replicaSpec)
	client := w.NewContainer(clientSpec)

	// Wait for the servers to be ready before testing DNS resolution
	servers.Await()

	// Verify that the group name resolves to exactly 3 IPs.
	// nslookup returns "Address" lines for both the DNS server (127.0.0.11)
	// and the results; filter out the server to count only result IPs.
	client.Exec([]string{"sh", "-c", fmt.Sprintf(
		`test "$(nslookup %s | grep 'Address' | grep -cv '127.0.0.11')" -eq 3`,
		servers.Name,
	)}, 0)

	// Each individual replica should also be reachable by its own name
	for _, pc := range servers.pending {
		client.Exec([]string{"ping", "-c", "1", pc.name}, 0)
	}
}

// TestReplicaHTTP tests replicas with a real HTTP server (caddy),
// verifying that the group name resolves to all replica IPs.
func TestReplicaHTTP(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	servers := w.NewContainer(ContainerSpec{
		Image:      "caddy:latest",
		Replicas:   3,
		WaitingFor: wait.ForHTTP("/").WithPort("80/tcp"),
	})

	client := w.NewContainer(ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "60"},
	})

	servers.Await()

	// Verify the group name resolves to 3 IPs
	client.Exec([]string{"sh", "-c", fmt.Sprintf(
		`test "$(nslookup %s | grep 'Address' | grep -cv '127.0.0.11')" -eq 3`,
		servers.Name,
	)}, 0)

	// Verify each individual replica is serving HTTP
	for _, pc := range servers.pending {
		client.Exec([]string{"wget", "-q", "-O", "/dev/null", fmt.Sprintf("http://%s:80/", pc.name)}, 0)
	}
}

// TestReplicaHTTP tests replicas with a real HTTP server (caddy),
// verifying that the group name resolves to all replica IPs.
func TestReplicaHTTPClients(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	servers := w.NewContainer(ContainerSpec{
		Image:      "caddy:latest",
		Replicas:   3,
		WaitingFor: wait.ForHTTP("/").WithPort("80/tcp"),
	})

	client := w.NewContainer(ContainerSpec{
		Image:     "curlimages/curl:latest",
		Replicas:  3,
		KeepAlive: true,
	})

	servers.Await()

	// Verify each individual replica is serving HTTP
	client.Exec([]string{"curl", fmt.Sprintf("http://%s:80/", servers.Name)}, 0)
}

// TestReplicaContextArchive verifies that each replica receives a complete
// ContextArchive when using FromDockerfile with Replicas > 1. Without the
// buffering fix, the first replica goroutine to start would exhaust the shared
// io.ReadSeeker, causing every other replica's image build to fail with an
// empty context.
func TestReplicaContextArchive(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	const fileContent = "built from context archive"

	// Build an in-memory tar archive containing a minimal Dockerfile.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dockerfile := "FROM alpine:latest\nRUN echo '" + fileContent + "' > /tmp/fromarchive.txt\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0o644,
		Size: int64(len(dockerfile)),
	}); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		t.Fatalf("failed to write tar entry: %v", err)
	}
	tw.Close()

	replicas := w.NewContainer(ContainerSpec{
		FromDockerfile: testcontainers.FromDockerfile{
			ContextArchive: bytes.NewReader(buf.Bytes()),
		},
		Cmd:      []string{"sleep", "60"},
		Replicas: 3,
	})

	// All three replicas must contain the file written during image build.
	replicas.Exec([]string{"grep", "-qF", fileContent, "/tmp/fromarchive.txt"}, 0)
}
