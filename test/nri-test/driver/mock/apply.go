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

package mock

import (
	"fmt"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/runtime-tools/generate"
)

// This file is the only place a realized spec changes. Everything a plugin
// asks for arrives here, and it is applied with the in-tree
// adjustment-to-spec mapping -- the same one runtimes vendor -- so that
// mapping is under test too.
//
// The rule that keeps this honest: an expectation is a value a test wrote
// down, never a second computation. A test that worked out what to expect by
// calling generate would compare this code against itself and could not
// fail. Assert on the value the test asked for.

// applyAdjustment applies a creation adjustment to a container's realized
// spec.
//
// A nil or empty adjustment is a no-op: that is what Adaptation returns when
// no plugin asked for anything.
func (r *Runtime) applyAdjustment(id string, adj *api.ContainerAdjustment) error {
	if adj == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("the mock runtime cannot adjust %s: no such container", id)
	}

	if err := generate.NewSpecGenerator(c.spec).Adjust(adj); err != nil {
		return fmt.Errorf("the mock runtime failed to adjust %s: %w", id, err)
	}

	return nil
}

// applyUpdates applies container updates to the model's realized specs.
//
// An update naming a container this runtime does not have is an error unless
// the plugin marked it ignorable, which is the protocol's own answer to a
// container that went away while the update was in flight.
func (r *Runtime) applyUpdates(updates []*api.ContainerUpdate) error {
	for _, u := range updates {
		if u == nil {
			continue
		}

		err := r.applyResources(u.GetContainerId(), u.GetLinux().GetResources())
		if err == nil {
			continue
		}

		if u.GetIgnoreFailure() {
			continue
		}

		return err
	}

	return nil
}

// applyResources applies a resource update to a container's realized spec.
func (r *Runtime) applyResources(id string, res *api.LinuxResources) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.containers[id]
	if !ok {
		return fmt.Errorf("the mock runtime cannot update %s: no such container", id)
	}

	if res == nil {
		return nil
	}

	if err := generate.NewSpecGenerator(c.spec).AdjustResources(res); err != nil {
		return fmt.Errorf("the mock runtime failed to update %s: %w", id, err)
	}

	return nil
}
