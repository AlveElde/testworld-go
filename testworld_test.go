package testworld

import (
	"fmt"
	"testing"
	"time"

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

	containers, err := wc.waitReady()
	if err != nil {
		t.Fatalf("Expected container creation to succeed: %v", err)
	}
	if len(containers) != 1 {
		t.Errorf("Expected 1 container, got %d", len(containers))
	}
	if containers[0] == nil {
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

// TestContainerStart tests that a container can be explicitly started.
func TestContainerStart(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	// Create container without starting
	wc := w.NewContainer(spec)

	// Explicitly start the container
	wc.Start()

	// Verify container is running by executing a command
	wc.Exec([]string{"echo", "hello"}, 0)
}

// TestContainerExec tests executing a command in a container.
func TestContainerExec(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image:   "alpine:latest",
		Cmd:     []string{"sleep", "30"},
		Started: true,
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
		Image:   "alpine:latest",
		Cmd:     []string{"sleep", "30"},
		Started: true,
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
		Image:   "alpine:latest",
		Cmd:     []string{"sh", "-c", "echo 'file content' > /tmp/testfile.txt && sleep 30"},
		Started: true,
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
		Started:   true,
		OnDestroy: onDestroy,
	}

	w.NewContainer(spec)

	// Destroy the world, which should trigger onDestroy
	w.Destroy()

	if !destroyCalled {
		t.Error("Expected onDestroy callback to be called")
	}
}

// TestNetworkConnectivity tests that containers in the same world can communicate.
func TestNetworkConnectivity(t *testing.T) {
	w := New(t, "./logs")
	defer w.Destroy()

	spec := ContainerSpec{
		Image:   "alpine:latest",
		Cmd:     []string{"sleep", "60"},
		Started: true,
	}

	wc1 := w.NewContainer(spec)

	wc2 := w.NewContainer(spec)

	// Test that container 1 can ping container 2 by name
	wc1.Exec([]string{"ping", "-c", "1", wc2.Name}, 0)
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
	containers, err := wc.waitReady()
	if err != nil {
		t.Fatalf("Expected replica creation to succeed: %v", err)
	}

	if len(containers) != 3 {
		t.Fatalf("Expected 3 containers, got %d", len(containers))
	}

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
		Started:  true,
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
		Started:  true,
		Replicas: 3,
	}

	clientSpec := ContainerSpec{
		Image:   "alpine:latest",
		Cmd:     []string{"sleep", "60"},
		Started: true,
		Awaited: true,
	}

	servers := w.NewContainer(replicaSpec)
	client := w.NewContainer(clientSpec)

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
		Started:    true,
		Awaited:    true,
		Replicas:   3,
		WaitingFor: wait.ForHTTP("/").WithPort("80/tcp"),
	})

	client := w.NewContainer(ContainerSpec{
		Image:   "alpine:latest",
		Cmd:     []string{"sleep", "60"},
		Started: true,
	})

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
