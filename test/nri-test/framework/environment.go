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
	"os"
	"runtime"
	"strings"
)

const kernelReleasePath = "/proc/sys/kernel/osrelease"

// Environment describes the driver a run used and the node it measured.
type Environment struct {
	Driver        string `json:"driver"`
	KernelRelease string `json:"kernelRelease"`
	Arch          string `json:"arch"`
}

// CurrentEnvironment returns the Environment of this process.
func CurrentEnvironment() Environment {
	driver := ""
	if TestConfig != nil {
		driver = TestConfig.Driver
	}
	return Environment{
		Driver:        driver,
		KernelRelease: kernelRelease(kernelReleasePath),
		Arch:          runtime.GOARCH,
	}
}

// kernelRelease returns the trimmed contents of path, or "" if unreadable.
func kernelRelease(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
