package testworld

import (
	"github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ContainerSpec defines the specification for creating a container.
type ContainerSpec struct {
	// Image is the container image to use (e.g., "alpine:latest")
	// If FromDockerfile is set, this is ignored.
	Image string

	// FromDockerfile allows building an image from a Dockerfile.
	FromDockerfile testcontainers.FromDockerfile

	// Started determines whether to start the container immediately after creation.
	// If false, call Start() on the returned container to start it.
	Started bool

	// Awaited determines whether NewContainer blocks until the container is created.
	// If false (the default), container creation happens in the background and
	// methods on WorldContainer transparently wait for it to be ready.
	// Set to true to make NewContainer block until the container exists.
	Awaited bool

	// Replicas is the number of identical containers to create.
	// When > 1, the WorldContainer represents a group of replicas.
	// Methods are executed on all replicas. The group name resolves
	// to all replica IPs via DNS round-robin.
	// Defaults to 1 if unset or zero.
	Replicas int

	// Entrypoint overrides the container's default entrypoint
	Entrypoint []string

	// Cmd is the command to run in the container
	Cmd []string

	// Env is a map of environment variables to set in the container
	Env map[string]string

	// ExposedPorts is a list of ports to expose (e.g., "80", "8080/tcp")
	ExposedPorts []string

	// Files is a list of files to copy into the container before it starts.
	Files []testcontainers.ContainerFile

	// Tmpfs is a map of tmpfs mounts (path -> options)
	Tmpfs map[string]string

	// ConfigModifier allows customizing the container config.
	ConfigModifier func(*container.Config)

	// HostConfigModifier allows customizing the Docker host config.
	HostConfigModifier func(*container.HostConfig)

	// WaitingFor is the strategy to wait for the container to be ready.
	WaitingFor wait.Strategy

	// OnDestroy is a callback function that is called before the container is terminated.
	OnDestroy func(WorldContainer)
}

// toGenericContainerRequest converts a ContainerSpec to a testcontainers.GenericContainerRequest.
func (spec ContainerSpec) toGenericContainerRequest(name, networkName string, aliases []string) testcontainers.GenericContainerRequest {
	return testcontainers.GenericContainerRequest{
		Started: spec.Started,
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile:     spec.FromDockerfile,
			Image:              spec.Image,
			Name:               name,
			Networks:           []string{networkName},
			NetworkAliases:     map[string][]string{networkName: aliases},
			Entrypoint:         spec.Entrypoint,
			Cmd:                spec.Cmd,
			Env:                spec.Env,
			ExposedPorts:       spec.ExposedPorts,
			Tmpfs:              spec.Tmpfs,
			WaitingFor:         spec.WaitingFor,
			Files:              spec.Files,
			ConfigModifier:     spec.ConfigModifier,
			HostConfigModifier: spec.HostConfigModifier,
		},
	}
}
