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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// A CDI spec for the input tests, and the one piece of node state outside a
// workload that this suite creates.
//
// A CDI device named on a CRI call has to resolve or the runtime refuses the
// creation, so testing that a runtime reports CRI-injected CDI devices to a
// plugin means there has to be a device. Three conditions make that acceptable,
// and none of them is optional:
//
//   - the file goes in /var/run/cdi, which is the transient directory, not
//     /etc/cdi, which is the node's own configuration;
//   - the vendor is namespaced to the tests, so the devices are recognisable as
//     ours on a shared node;
//   - the devices are harmless -- /dev/null and /dev/zero under names of our
//     own -- so a fixture that outlived its run could not hand a container
//     anything that matters.
//
// The file is run-scoped and removed by the run that wrote it. Nothing else in
// /var/run/cdi is ever touched: the directory belongs to whatever else declares
// devices on this node, and the suite's standing rule is that it never destroys
// node state it did not create.

const (
	// CDIVendor namespaces the test devices. It matches the annotation
	// namespace the suite already uses.
	CDIVendor = "nri-test.containerd.io"
	// CDIClass is the device class under that vendor.
	CDIClass = "device"
	// cdiSpecVersion is the CDI spec version the file declares. 0.6.0 is old
	// enough for every runtime this suite targets to parse.
	cdiSpecVersion = "0.6.0"
)

// cdiDir is where the spec file goes. A variable so the unit tests can point it
// at a temporary directory and run without root.
var cdiDir = "/var/run/cdi"

// cdiDirCreated records that WriteCDISpec had to create cdiDir, so that
// RemoveCDISpec can take away a directory the suite made and leave alone one
// that was already there. A node with no CDI devices has no /var/run/cdi at
// all, so this is the common case rather than the corner.
var cdiDirCreated bool

// cdiDevices are the devices the spec declares: a host device that cannot
// matter, exposed under a name that cannot collide with a real one.
var cdiDevices = map[string]string{
	"null": "/dev/null",
	"zero": "/dev/zero",
}

// CDIDeviceName returns the fully qualified CDI name of a test device, as a CRI
// ContainerConfig and an NRI Container both carry it.
func CDIDeviceName(device string) string {
	return fmt.Sprintf("%s/%s=%s", CDIVendor, CDIClass, device)
}

// CDISpecPath returns where the run's CDI spec file lives.
//
// The run id is in the file name, and that is what makes teardown safe: two
// runs of the suite can share a node, and a run removing "the test CDI spec"
// rather than its own would pull the device out from under a live peer.
func CDISpecPath(runID string) string {
	return filepath.Join(cdiDir, "nri-test-"+runID+".json")
}

// WriteCDISpec writes the run's CDI spec, creating the directory if the node
// has none.
func WriteCDISpec(runID string) error {
	type deviceNode struct {
		Path     string `json:"path"`
		HostPath string `json:"hostPath"`
	}

	type containerEdits struct {
		DeviceNodes []deviceNode `json:"deviceNodes"`
	}

	type device struct {
		Name           string         `json:"name"`
		ContainerEdits containerEdits `json:"containerEdits"`
	}

	spec := struct {
		CDIVersion string   `json:"cdiVersion"`
		Kind       string   `json:"kind"`
		Devices    []device `json:"devices"`
	}{
		CDIVersion: cdiSpecVersion,
		Kind:       CDIVendor + "/" + CDIClass,
	}

	for name, host := range cdiDevices {
		spec.Devices = append(spec.Devices, device{
			Name: name,
			ContainerEdits: containerEdits{
				DeviceNodes: []deviceNode{{
					// The container path is deliberately not the host path: a
					// device appearing under its usual name would be
					// indistinguishable from one the image or the runtime
					// provided, and the test asserts on this name.
					Path:     "/dev/test-" + name,
					HostPath: host,
				}},
			},
		})
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to encode the test CDI spec: %w", err)
	}

	// Noted before creating it, not after: what matters is whether the
	// directory was ours to make, and MkdirAll cannot say.
	if _, err := os.Stat(cdiDir); os.IsNotExist(err) {
		cdiDirCreated = true
	}

	if err := os.MkdirAll(cdiDir, 0o755); err != nil {
		return fmt.Errorf("failed to create %s for the test CDI spec: %w", cdiDir, err)
	}

	path := CDISpecPath(runID)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("failed to write the test CDI spec %s: %w", path, err)
	}

	return nil
}

// RemoveCDISpec removes the run's own CDI spec and nothing else.
//
// Exactly one path, the one CDISpecPath names. Not a glob and not a sweep of
// the directory: /var/run/cdi is where anything on the node may declare a
// device, and a teardown that swept it would take a real device away from a
// real workload. An absent file is not an error, because teardown runs after a
// Setup that may have failed before writing it.
func RemoveCDISpec(runID string) error {
	if err := os.Remove(CDISpecPath(runID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove the test CDI spec %s: %w",
			CDISpecPath(runID), err)
	}

	// And the directory, but only if this run created it. os.Remove refuses a
	// non-empty directory, which is the second guard: another run's spec or a
	// real vendor's keeps it, and neither is ours to sweep. A failure here is
	// not worth reporting -- the spec, which is the thing that mattered, is
	// already gone.
	if cdiDirCreated {
		if err := os.Remove(cdiDir); err == nil {
			cdiDirCreated = false
		}
	}

	return nil
}
