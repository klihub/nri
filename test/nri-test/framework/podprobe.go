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
	"fmt"
	"strings"
)

// systemdCgroupPathError is what a low-level runtime configured for systemd
// cgroups reports when it is handed a cgroupfs-form path.
const systemdCgroupPathError = "expected cgroupsPath to be of format"

// checkPodCreation fails Setup when the runtime refuses a pod created the way
// this run's configuration asks for one.
func checkPodCreation(ctx context.Context, cfg *Config) error {
	spec := &PodSpec{
		Name:      "nri-test-preflight-" + cfg.RunID,
		Namespace: cfg.Namespace,
		UID:       cfg.RunID + "-preflight",
		// The run's own id, so that the sweeps and the leak guard recognise this
		// pod as this run's and remove it if the probe cannot.
		Labels:         map[string]string{RunIDLabel: cfg.RunID},
		Annotations:    map[string]string{RunIDLabel: cfg.RunID},
		RuntimeHandler: cfg.RuntimeHandler,
		CgroupParent:   cfg.CgroupParent,
	}

	pod, err := driver.CreatePod(ctx, spec)
	if err != nil {
		return diagnosePodCreation(err, cfg)
	}

	if err := driver.StopPod(ctx, pod); err != nil {
		return fmt.Errorf("the runtime could not stop this run's probe pod: %w", err)
	}

	return driver.RemovePod(ctx, pod)
}

// diagnosePodCreation returns err with what a reader can act on, for the
// refusals whose cause the message alone does not give.
func diagnosePodCreation(err error, cfg *Config) error {
	if cfg.CgroupParent == "" && strings.Contains(err.Error(), systemdCgroupPathError) {
		return fmt.Errorf("the runtime refused this run's first pod because it "+
			"expects a systemd cgroup path:\n\n  %w\n\n"+
			"This runtime is configured for systemd cgroups, and this run named "+
			"no cgroup parent, so the path the runtime derived is in the wrong "+
			"form. Re-run with -nri-test.cgroup-parent=system.slice", err)
	}

	return fmt.Errorf("the runtime refused this run's first pod: %w", err)
}
