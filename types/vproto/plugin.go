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

package vproto

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Path returns the currently resolved path for the plugin.
func (p *Plugin) Path() string {
	return p.path
}

// Resolve tries to resolve the path to the plugin.
func (p *Plugin) Resolve(searchPath ...string) string {
	if p.path != "" {
		if _, err := os.Stat(p.path); err == nil {
			return p.path
		}
		p.path = ""
	}

	for _, path := range searchPath {
		if path != "" {
			path = filepath.Join(path, p.Type)
		} else {
			path = p.Type
		}

		if _, err := exec.LookPath(path); err == nil {
			p.path = path
			break
		}
	}

	return p.path
}
