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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// kubeletComm is the name the kernel records for a kubelet process.
const kubeletComm = "kubelet"

// kubeletRunning reports whether a kubelet process is running, and the pid it
// was found at. procDir is the proc filesystem to scan, normally /proc.
//
// Detection reads /proc/<pid>/comm, which every user can read, so it works
// without root. Only directories whose names are entirely decimal are
// considered: /proc/self and /proc/thread-self are this process, not a second
// one. A comm that cannot be read is skipped without an error, because other
// users' processes are the ordinary case on a shared node. A procDir that
// cannot be read at all reports no kubelet.
//
// False positive: any process whose comm is "kubelet" matches, whoever runs
// it and whatever it does. -nri-test.allow-kubelet exists for that case.
func kubeletRunning(procDir string) (bool, string) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return false, ""
	}

	for _, e := range entries {
		pid := e.Name()
		if !isDecimal(pid) {
			continue
		}

		comm, err := os.ReadFile(filepath.Join(procDir, pid, "comm"))
		if err != nil {
			continue
		}

		if strings.TrimSpace(string(comm)) == kubeletComm {
			return true, pid
		}
	}

	return false, ""
}

// kubeletConflicter is implemented by a driver whose workloads kubelet would
// garbage-collect: kubelet does not recognise a sandbox created outside it, and
// a sandbox vanishing mid-test reads as a runtime failure.
//
// Optional, and discovered by type assertion, exactly as versioner and
// imagePuller are. A driver that does not implement it is not sensitive. That
// is right for a future Kubernetes driver, where kubelet running is the
// precondition rather than the hazard.
type kubeletConflicter interface {
	ConflictsWithKubelet() bool
}

// checkKubelet refuses a run whose driver conflicts with kubelet while a kubelet
// process is running, unless cfg.AllowKubelet is set. It neither stops kubelet
// nor reconfigures anything: stopping and restarting it is a wrapper script's
// job.
func checkKubelet(cfg *Config, d Driver) error {
	if cfg.AllowKubelet {
		return nil
	}

	c, ok := d.(kubeletConflicter)
	if !ok || !c.ConflictsWithKubelet() {
		return nil
	}

	procDir := cfg.procDir
	if procDir == "" {
		procDir = "/proc"
	}

	found, pid := kubeletRunning(procDir)
	if !found {
		return nil
	}

	// The driver is named rather than the mode. This check is driver-agnostic --
	// it asks the driver whether it conflicts -- and naming CRI here would be wrong
	// the first day a second conflicting driver exists, which is exactly the day
	// nobody rereads this string.
	return fmt.Errorf("kubelet is running (pid %s), and a run with driver %q would "+
		"have its pod sandboxes garbage-collected by kubelet mid-test, which reads "+
		"as a runtime failure; stop kubelet, or pass -nri-test.allow-kubelet to run "+
		"anyway and accept that sandboxes may be collected mid-run", pid, d.Name())
}

// isDecimal reports whether s is non-empty and made only of ASCII digits.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
