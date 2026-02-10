package testworld

import (
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/wait"
)

// TestWorldCreation tests that a World can be created and destroyed properly.
func TestWorldCreation(t *testing.T) {
	w := New(t, "")
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
	w := New(t, "")
	defer w.Destroy()

	spec := ContainerSpec{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "30"},
	}

	wc := w.NewContainer(spec)

	if wc.Name == "" {
		t.Error("Expected container name to be non-empty")
	}

	if wc.container == nil {
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
	w := New(t, "")
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
	w := New(t, "")
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
	w := New(t, "")
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
	w := New(t, "")
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
	w := New(t, "")

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
	w := New(t, "")
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
