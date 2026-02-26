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

	// Isolated blocks internet access for this container. The container can
	// still communicate with other containers in the same testworld, but all
	// traffic to external networks is dropped.
	Isolated bool

	// Aliases adds extra DNS aliases for this container, making it reachable
	// by additional names from other containers in the world.
	Aliases []string

	// FromDockerfile allows building an image from a Dockerfile.
	FromDockerfile testcontainers.FromDockerfile

	// Replicas is the number of identical containers to create.
	// When > 1, the WorldContainer represents a group of replicas.
	// Methods are executed on all replicas. The group name resolves
	// to all replica IPs via DNS round-robin.
	// Defaults to 1 if unset or zero.
	Replicas int

	// Entrypoint overrides the container's default entrypoint
	Entrypoint []string

	// KeepAlive keeps the container running indefinitely. When set and no Cmd
	// is provided, "sleep infinity" is used as the command.
	KeepAlive bool

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

	// Requires declares that this container depends on the listed containers
	// being ready before creation starts. If any dependency fails, this
	// container also fails without being created.
	Requires []WorldContainer

	// After is like Requires but only delays method calls (Await, Exec, etc.),
	// not container creation. The container is created in parallel with its
	// After dependencies, but any method blocks until they are ready.
	After []WorldContainer

	// OnDestroy is a callback function that is called before the container is terminated.
	OnDestroy func(WorldContainer)
}

// toGenericContainerRequest converts a ContainerSpec to a testcontainers.GenericContainerRequest.
// All containers join the internal network so they can communicate with each other via DNS.
// Non-isolated containers also join the external network, gaining internet access.
// Isolated containers join only the internal network, blocking internet access.
func (spec ContainerSpec) toGenericContainerRequest(name, externalNetwork, internalNetwork string, aliases []string) testcontainers.GenericContainerRequest {
	var networks []string
	networkAliases := map[string][]string{internalNetwork: aliases}

	if spec.Isolated {
		networks = []string{internalNetwork}
	} else {
		networks = []string{externalNetwork, internalNetwork}
		networkAliases[externalNetwork] = aliases
	}

	cmd := spec.Cmd
	if spec.KeepAlive && len(cmd) == 0 {
		cmd = []string{"sleep", "infinity"}
	}

	return testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile:     spec.FromDockerfile,
			Image:              spec.Image,
			Name:               name,
			Networks:           networks,
			NetworkAliases:     networkAliases,
			Entrypoint:         spec.Entrypoint,
			Cmd:                cmd,
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
