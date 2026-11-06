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
	"testing"

	"github.com/stretchr/testify/require"
)

// procSelfStatus is a fragment of a real /proc/self/status, read inside a
// busybox container created with CpusetCpus "0-1" on a 44-CPU host. The
// separators are what they are in the file itself: a tab after most keys, a
// space after a few. The last line is deliberately malformed.
const procSelfStatus = `Name:	sleep
Umask:	0022
State:	S (sleeping)
Tgid:	1
Pid:	1
PPid:	0
Cpus_allowed:	00000000,00000003
Cpus_allowed_list:	0-1
Mems_allowed_list:	0
Seccomp:	2
Seccomp_filters:	1
Speculation_Store_Bypass:	thread vulnerable
nonvoluntary_ctxt_switches:	1
this line has no colon at all
`

func TestProcStatus(t *testing.T) {
	t.Run("should parse tab separated values", func(t *testing.T) {
		status := ProcStatus(procSelfStatus)

		// The subject of the in-container CPU affinity assertions: the value keeps
		// none of the tab that separated it from its key.
		require.Equal(t, "0-1", status["Cpus_allowed_list"])
		require.Equal(t, "0", status["Mems_allowed_list"])
		require.Equal(t, "00000000,00000003", status["Cpus_allowed"])
		require.Equal(t, "2", status["Seccomp"])
		require.Equal(t, "1", status["Seccomp_filters"])
	})

	t.Run("should parse the Name line", func(t *testing.T) {
		// Name is how a test tells whose status it is reading: /proc/1/status in a
		// pod-shared PID namespace reports "pause", not the test's own process.
		require.Equal(t, "sleep", ProcStatus(procSelfStatus)["Name"])
	})

	t.Run("should keep a value containing spaces", func(t *testing.T) {
		require.Equal(t, "S (sleeping)", ProcStatus(procSelfStatus)["State"])
		require.Equal(t, "thread vulnerable",
			ProcStatus(procSelfStatus)["Speculation_Store_Bypass"])
	})

	t.Run("should skip a malformed line rather than panic", func(t *testing.T) {
		status := ProcStatus(procSelfStatus)

		require.NotContains(t, status, "this line has no colon at all")
		// The malformed line must not take the rest of the file with it: the field
		// after it is still there.
		require.Equal(t, "1", status["nonvoluntary_ctxt_switches"])
	})

	t.Run("should tolerate empty and degenerate input", func(t *testing.T) {
		require.NotNil(t, ProcStatus(""), "the result is always usable, never nil")
		require.Empty(t, ProcStatus(""))
		require.Empty(t, ProcStatus("\n\n\n"))
		require.Empty(t, ProcStatus("no colons here\nnor here\n"))
		require.Empty(t, ProcStatus(":	orphan value"),
			"a line with an empty key carries no usable field")
	})

	t.Run("should record a field with an empty value", func(t *testing.T) {
		// An empty value is an answer in its own right, so it is kept rather than
		// dropped: a caller distinguishing "absent" from "empty" needs both.
		status := ProcStatus("Groups:\t\nCpus_allowed_list:\t0\n")

		require.Contains(t, status, "Groups")
		require.Empty(t, status["Groups"])
		require.Equal(t, "0", status["Cpus_allowed_list"])
	})
}
