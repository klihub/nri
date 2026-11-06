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
	"google.golang.org/protobuf/proto"

	"github.com/containerd/nri/pkg/api"
)

// requireTableCovers fails naming any field of m that the input table neither
// verifies nor explicitly puts out of scope.
//
// This is what keeps the table honest as pkg/api grows. A field added to the
// protocol and to neither list is a field no test looks at and no document
// admits to skipping, which is the silent half of the coverage problem this
// table exists to solve -- the loud half, a field in the table that no run
// exercised, is the run summary's job.
func requireTableCovers(t *testing.T, message string, m proto.Message) {
	t.Helper()

	inTable := map[string]bool{}
	for _, f := range InputFields {
		if f.Message == message {
			inTable[f.Name] = true
		}
	}

	fds := m.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fds.Len(); i++ {
		name := string(fds.Get(i).Name())

		if inTable[name] {
			continue
		}

		reason, excluded := inputFieldsOutOfScope[message][name]
		require.True(t, excluded,
			"%s.%s is in neither InputFields nor inputFieldsOutOfScope: a new "+
				"protocol field has to be classified, not silently ignored",
			message, name)
		require.NotEmpty(t, reason,
			"%s.%s is out of scope with no reason given", message, name)
	}
}

func TestInputTableCoversPodSandbox(t *testing.T) {
	requireTableCovers(t, "PodSandbox", &api.PodSandbox{})
}

func TestInputTableCoversLinuxPodSandbox(t *testing.T) {
	requireTableCovers(t, "LinuxPodSandbox", &api.LinuxPodSandbox{})
}

func TestInputTableCoversContainer(t *testing.T) {
	requireTableCovers(t, "Container", &api.Container{})
}

func TestInputTableCoversLinuxContainer(t *testing.T) {
	requireTableCovers(t, "LinuxContainer", &api.LinuxContainer{})
}

func TestInputTableHasNoUnknownFields(t *testing.T) {
	// The converse of the coverage tests: a row naming a field that does not
	// exist would be a row no run can ever exercise, reported forever as
	// unmeasured. Catches a typo and a field renamed upstream.
	known := map[string]map[string]bool{}
	for message, m := range map[string]proto.Message{
		"PodSandbox":      &api.PodSandbox{},
		"LinuxPodSandbox": &api.LinuxPodSandbox{},
		"Container":       &api.Container{},
		"LinuxContainer":  &api.LinuxContainer{},
	} {
		known[message] = map[string]bool{}

		fds := m.ProtoReflect().Descriptor().Fields()
		for i := 0; i < fds.Len(); i++ {
			known[message][string(fds.Get(i).Name())] = true
		}
	}

	for _, f := range InputFields {
		require.Contains(t, known, f.Message,
			"InputFields names message %q, which is not an input message", f.Message)
		require.True(t, known[f.Message][f.Name],
			"InputFields names %s.%s, which the protocol does not have",
			f.Message, f.Name)
	}

	for message, fields := range inputFieldsOutOfScope {
		require.Contains(t, known, message,
			"inputFieldsOutOfScope names message %q, which is not an input message", message)

		for name := range fields {
			require.True(t, known[message][name],
				"inputFieldsOutOfScope names %s.%s, which the protocol does not have",
				message, name)
		}
	}
}

func TestInputTableNamesEveryRowOnce(t *testing.T) {
	// A field in the table twice would be verified twice and counted twice, and
	// a field in both lists would be verified and excused at once.
	seen := map[string]bool{}
	for _, f := range InputFields {
		key := f.Message + "." + f.Name
		require.False(t, seen[key], "%s appears in InputFields more than once", key)
		seen[key] = true
	}

	for message, fields := range inputFieldsOutOfScope {
		for name := range fields {
			key := message + "." + name
			require.False(t, seen[key],
				"%s is both verified and out of scope", key)
		}
	}
}

func TestInputFieldsAtRequiredHook(t *testing.T) {
	names := func(hook EventType) map[string]bool {
		got := map[string]bool{}
		for _, f := range InputFieldsAt(hook) {
			require.Equal(t, hook, f.RequiredAt)
			got[f.Message+"."+f.Name] = true
		}

		return got
	}

	t.Run("should group the creation-time fields under CreateContainer", func(t *testing.T) {
		at := names(CreateContainer)
		require.True(t, at["Container.CDI_devices"])
		require.True(t, at["LinuxContainer.sysctl"])
		require.False(t, at["Container.started_at"],
			"a container has not started at CreateContainer")
		require.False(t, at["Container.pid"],
			"a container has no process at CreateContainer")
	})

	t.Run("should group the start-time fields under PostStartContainer", func(t *testing.T) {
		at := names(PostStartContainer)
		require.True(t, at["Container.started_at"])
		require.True(t, at["Container.pid"])
	})

	t.Run("should group the pod fields under RunPodSandbox", func(t *testing.T) {
		at := names(RunPodSandbox)
		require.True(t, at["PodSandbox.ips"])
		require.True(t, at["PodSandbox.pid"])
		require.True(t, at["LinuxPodSandbox.cgroup_parent"])
	})

	t.Run("should group the exit fields under RemoveContainer", func(t *testing.T) {
		// StopContainer is a request hook: it fires before the container is
		// stopped, so it is the one hook that cannot carry an exit code.
		at := names(RemoveContainer)
		require.True(t, at["Container.exit_code"])
		require.True(t, at["Container.finished_at"])
	})

	t.Run("should return nothing for a hook with no fields of its own", func(t *testing.T) {
		require.Empty(t, InputFieldsAt(Configured))
	})
}

func TestInputFieldTiers(t *testing.T) {
	// Every row has to say how it is checked, or the test generated from it
	// cannot know which comparison to make. The zero value is deliberately not
	// a valid tier for that reason.
	for _, f := range InputFields {
		require.Contains(t,
			[]InputTier{TierFidelity, TierAgreement, TierState}, f.Tier,
			"%s.%s has no tier", f.Message, f.Name)
		require.NotEmpty(t, f.RequiredAt,
			"%s.%s has no required hook", f.Message, f.Name)
	}
}
