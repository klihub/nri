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

package framework

import (
	"context"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
)

// Observed is a container's realized state, as the runtime has it.
type Observed struct {
	// Spec is the container's OCI runtime spec.
	Spec *rspec.Spec
	// State is the container's lifecycle state.
	State ContainerState
	// Pid is the container's init process, or zero when the runtime reports
	// none. A container inspected before it is started has no process yet, so
	// zero is an ordinary answer here and not an error -- a test that needs a
	// pid asserts on it rather than assuming Inspect refused.
	Pid uint32
	// Reason and Message are why the container terminated, as CRI reports it.
	// Both are empty for a container that has not terminated, and a runtime
	// may leave them empty for one that has.
	Reason  string
	Message string
}

// ObservedPod is a pod sandbox's realized state.
//
// Separate from Observed rather than a nullable half of it: the two are read
// from different CRI calls, and a type that could hold either would let a
// caller read the half that was never filled in.
type ObservedPod struct {
	// IPs are the sandbox's addresses, the primary one first. Empty for a
	// host-network sandbox, which has no network status of its own.
	IPs []string
	// Pid is the sandbox's own process, or zero when the runtime reports none.
	Pid uint32
	// Spec is the sandbox's OCI runtime spec, or nil when the runtime reports
	// none. It is the only oracle for what the runtime realized for the pod
	// itself, as opposed to for a container in it.
	Spec *rspec.Spec
}

// Inspector observes a container's realized state.
//
// It is keyed by a bare container id rather than by a driver-specific handle,
// deliberately: the id is available in every mode, so the CRI inspector can be
// reused by a Kubernetes driver without change.
type Inspector interface {
	// Inspect returns the container's realized state.
	Inspect(ctx context.Context, containerID string) (*Observed, error)
	// InspectPod returns the pod sandbox's realized state.
	//
	// Here rather than on Driver for the reason the whole type is: this is
	// observation, and a pod's addresses are what an input test compares the
	// PodSandbox a plugin was shown against.
	InspectPod(ctx context.Context, podID string) (*ObservedPod, error)
}

// Executor runs a command inside a running container and returns its output.
// Because exec joins the container's cgroups and namespaces, what the command
// observes is what the container actually got -- which is why this is the
// authoritative surface for anything the kernel exposes about itself.
//
// Keyed by a bare container id for the same reason Inspector is.
//
// An implementation returns the command's stdout and stderr whether it
// succeeded or not, and reports a non-zero exit as an error: a test asserting
// that a command failed needs both the error and what the command printed
// before it did.
type Executor interface {
	Exec(ctx context.Context, containerID string, cmd []string) (stdout, stderr string, err error)
}
