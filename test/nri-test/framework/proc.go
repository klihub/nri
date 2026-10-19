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

import "strings"

// ProcStatus parses the key/value lines of a /proc/<pid>/status file.
//
// Every line is "Key:" followed by whitespace and a value — the separator is a
// tab for most fields and a space for a few, so the value is split off at the
// colon and trimmed rather than matched against any particular whitespace. Keys
// keep their names without the trailing colon, so a caller asks for
// "Cpus_allowed_list", exactly as the file spells it.
//
// A line with no colon in it is skipped, not guessed at: the file gains fields
// between kernel versions, and a suite that panicked or invented a key on
// something it did not recognize would fail on a newer kernel for a reason that
// has nothing to do with NRI. Values are kept verbatim otherwise, including the
// empty ones, since an empty value is itself an answer.
func ProcStatus(out string) map[string]string {
	status := map[string]string{}

	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}

		status[key] = strings.TrimSpace(value)
	}

	return status
}
