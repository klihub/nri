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
}

// Inspector observes a container's realized state.
//
// It is keyed by a bare container id rather than by a driver-specific handle,
// deliberately: the id is available in every mode, so the CRI inspector can be
// reused by a Kubernetes driver without change.
type Inspector interface {
	// Inspect returns the container's realized state.
	Inspect(ctx context.Context, containerID string) (*Observed, error)
}

// Executor runs a command inside a running container and returns its output.
// Because exec joins the container's cgroups and namespaces, what the command
// observes is what the container actually got — which is why this is the
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
