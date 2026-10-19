/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package cri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

// verboseInfo is the part of a runtime's verbose container info we need. Both
// containerd and CRI-O expose the container's OCI spec under runtimeSpec at the
// top level; their other fields differ and are ignored.
type verboseInfo struct {
	RuntimeSpec *rspec.Spec `json:"runtimeSpec"`
}

// ParseVerboseInfo extracts a container's OCI spec from a verbose
// ContainerStatus response's Info map.
func ParseVerboseInfo(info map[string]string) (*rspec.Spec, error) {
	raw, ok := info["info"]
	if !ok {
		return nil, fmt.Errorf("verbose container status has no %q key; got keys %v",
			"info", keysOf(info))
	}

	v := &verboseInfo{}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return nil, fmt.Errorf("failed to decode verbose container info: %w", err)
	}

	if v.RuntimeSpec == nil {
		return nil, errors.New("verbose container info has no runtimeSpec")
	}

	return v.RuntimeSpec, nil
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

// Inspector observes containers over CRI.
//
// It is both the framework's Inspector and its Executor. The two belong on one
// type because the Driver/Inspector split is about mutation versus observation,
// and running a command inside a container to read what the kernel applied is
// observation -- it changes nothing about the container. A separate Executor type
// would need the same *Client, be constructed at the same moment from the same
// place, and be handed through the same seam, so it would add a type and a
// constructor and buy nothing.
type Inspector struct {
	client *Client
}

var (
	_ framework.Inspector = &Inspector{}
	_ framework.Executor  = &Inspector{}
)

// NewInspector returns an Inspector using the given client.
func NewInspector(c *Client) *Inspector {
	return &Inspector{client: c}
}

// Inspect returns the container's realized state.
func (i *Inspector) Inspect(ctx context.Context, containerID string) (*framework.Observed, error) {
	rpl, err := i.client.Runtime.ContainerStatus(ctx, containerID, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get status of container %s: %w", containerID, err)
	}

	spec, err := ParseVerboseInfo(rpl.GetInfo())
	if err != nil {
		return nil, fmt.Errorf("container %s: %w", containerID, err)
	}

	o := &framework.Observed{
		Spec:  spec,
		State: containerState(rpl.GetStatus()),
	}

	return o, nil
}

// Exec runs a command inside a running container and returns its output.
//
// The command's timeout is taken from ctx's deadline rather than from a
// configured value of our own, so that the runtime's own limit on the command and
// the caller's limit on the RPC are the same number. Suite.TryExec always
// installs one. Without a deadline the timeout is zero, which CRI defines as no
// timeout -- correct, if the caller really means to wait indefinitely, and the
// only honest reading of a context that says so.
//
// Both streams are returned even on error: ExecSync reports a non-zero exit as an
// error and still returns what the command wrote, and stderr is where a shell
// says why it failed.
func (i *Inspector) Exec(ctx context.Context, containerID string, cmd []string) (string, string, error) {
	var timeout time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}

	stdout, stderr, err := i.client.Runtime.ExecSync(ctx, containerID, cmd, timeout)
	if err != nil {
		return string(stdout), string(stderr), fmt.Errorf(
			"failed to exec %v in container %s: %w", cmd, containerID, err)
	}

	return string(stdout), string(stderr), nil
}

// containerState maps a CRI container status onto a framework state.
//
// A nil status is unknown, not created: GetState() on a nil status yields the
// enum's zero value, CONTAINER_CREATED, so a response missing its status would
// otherwise be reported as a created container.
func containerState(s *runtimeapi.ContainerStatus) framework.ContainerState {
	if s == nil {
		return framework.StateUnknown
	}

	switch s.GetState() {
	case runtimeapi.ContainerState_CONTAINER_CREATED:
		return framework.StateCreated
	case runtimeapi.ContainerState_CONTAINER_RUNNING:
		return framework.StateRunning
	case runtimeapi.ContainerState_CONTAINER_EXITED:
		return framework.StateExited
	}

	return framework.StateUnknown
}
