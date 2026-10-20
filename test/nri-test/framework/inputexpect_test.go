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

	"github.com/containerd/nri/pkg/api"
)

func TestExpectStringsSubset(t *testing.T) {
	t.Run("should accept entries the runtime added", func(t *testing.T) {
		// NRI projects the OCI spec rather than copying it, and a runtime adds
		// defaults of its own. Equality would fail every runtime on things that
		// are not defects.
		require.NoError(t, ExpectStringsSubset("namespaces",
			[]string{"pid"}, []string{"pid", "mount", "network"}))
	})

	t.Run("should accept both empty", func(t *testing.T) {
		require.NoError(t, ExpectStringsSubset("namespaces", nil, nil))
	})

	t.Run("should reject a value the runtime never realized", func(t *testing.T) {
		err := ExpectStringsSubset("namespaces",
			[]string{"pid", "invented"}, []string{"pid"})
		require.ErrorContains(t, err, "invented")
		require.ErrorContains(t, err, "namespaces", "the error must name the field")
	})

	t.Run("should reject every missing value, not only the first", func(t *testing.T) {
		// A field with two wrong entries is one finding with two names in it: a
		// reader of the summary fixes what the message lists, and a message
		// that stopped at the first would send them back for the second.
		err := ExpectStringsSubset("env",
			[]string{"A=1", "B=2"}, []string{"C=3"})
		require.ErrorContains(t, err, "A=1")
		require.ErrorContains(t, err, "B=2")
	})
}

func TestExpectMapSubset(t *testing.T) {
	t.Run("should accept keys the runtime added", func(t *testing.T) {
		require.NoError(t, ExpectMapSubset("annotations",
			map[string]string{"a": "1"},
			map[string]string{"a": "1", "runtime-added": "yes"}))
	})

	t.Run("should reject a missing key", func(t *testing.T) {
		err := ExpectMapSubset("annotations",
			map[string]string{"a": "1"}, map[string]string{"b": "2"})
		require.ErrorContains(t, err, "a")
	})

	t.Run("should reject a present key with the wrong value", func(t *testing.T) {
		// A key whose value disagrees is not a subset. Checking only for the
		// key's presence is the version of this that cannot fail.
		err := ExpectMapSubset("sysctl",
			map[string]string{"net.ipv4.ip_forward": "1"},
			map[string]string{"net.ipv4.ip_forward": "0"})
		require.ErrorContains(t, err, "net.ipv4.ip_forward")
		require.ErrorContains(t, err, "1")
		require.ErrorContains(t, err, "0")
	})
}

func TestExpectScalarEqual(t *testing.T) {
	t.Run("should accept equal values", func(t *testing.T) {
		require.NoError(t, ExpectScalarEqual("oom_score_adj", -17, -17))
		require.NoError(t, ExpectScalarEqual("cgroups_path", "a/b", "a/b"))
	})

	t.Run("should reject a mismatch naming both sides", func(t *testing.T) {
		err := ExpectScalarEqual("cgroups_path", "shown", "realized")
		require.ErrorContains(t, err, "shown")
		require.ErrorContains(t, err, "realized")
		require.ErrorContains(t, err, "cgroups_path")
	})
}

func TestExpectRequired(t *testing.T) {
	// ExpectPopulated cannot serve for a field with no oracle: its message
	// names what the runtime realized, and a caller with nothing to read from
	// has to pass a count it invented. Three callers did, each passing a
	// literal 1, so a run reported "the container has 1" about a value nobody
	// had looked up.
	t.Run("should reject a field the runtime left unset", func(t *testing.T) {
		err := ExpectRequired("pid", 0)
		require.ErrorContains(t, err, "pid")
		require.ErrorContains(t, err, "protocol",
			"the claim rests on the field table, so the message has to say so")
	})

	t.Run("should accept any value at all", func(t *testing.T) {
		require.NoError(t, ExpectRequired("pid", 1))
		require.NoError(t, ExpectRequired("pid", 4242))
	})

	// It must not claim anything about a container, which is the whole reason
	// it is not ExpectPopulated.
	t.Run("should not claim the runtime realized a value", func(t *testing.T) {
		err := ExpectRequired("started_at", 0)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "the container has")
	})
}

func TestExpectPopulated(t *testing.T) {
	t.Run("should reject an empty view of something the runtime realized", func(t *testing.T) {
		// The reason this function exists. Subset alone cannot catch omission,
		// because the empty set is a subset of anything -- so a runtime that
		// tells a plugin nothing at all would pass every subset check in the
		// suite. Omission is the defect class this work was built for.
		err := ExpectPopulated("CDI_devices", 0, 3)
		require.ErrorContains(t, err, "CDI_devices")
		require.ErrorContains(t, err, "3", "the error must say what the runtime did realize")
	})

	t.Run("should accept both empty", func(t *testing.T) {
		// Nothing was there to report, so nothing was omitted.
		require.NoError(t, ExpectPopulated("CDI_devices", 0, 0))
	})

	t.Run("should accept a populated view", func(t *testing.T) {
		require.NoError(t, ExpectPopulated("CDI_devices", 1, 1))
	})

	t.Run("should accept a view with more than the runtime realized", func(t *testing.T) {
		// Not this function's question. A view carrying what the runtime did not
		// realize is fabrication, which the subset check is for.
		require.NoError(t, ExpectPopulated("CDI_devices", 3, 1))
	})
}

func TestExpectStateAt(t *testing.T) {
	t.Run("should want the state each hook implies", func(t *testing.T) {
		for hook, want := range map[EventType]api.ContainerState{
			PostCreateContainer: api.ContainerState_CONTAINER_CREATED,
			StartContainer:      api.ContainerState_CONTAINER_CREATED,
			PostStartContainer:  api.ContainerState_CONTAINER_RUNNING,
			RemoveContainer:     api.ContainerState_CONTAINER_STOPPED,
		} {
			require.NoError(t, ExpectStateAt(hook, want), hook)
		}
	})

	t.Run("should reject a state the hook contradicts", func(t *testing.T) {
		err := ExpectStateAt(PostStartContainer, api.ContainerState_CONTAINER_CREATED)
		require.ErrorContains(t, err, "PostStartContainer")
		require.ErrorContains(t, err, "CONTAINER_CREATED")
		require.ErrorContains(t, err, "CONTAINER_RUNNING")
	})

	t.Run("should reject an unknown state at every hook", func(t *testing.T) {
		// CONTAINER_UNKNOWN is the zero value, so a runtime that left the field
		// alone reports it. That is the failure this ladder is here to catch.
		for _, hook := range []EventType{
			PostCreateContainer, StartContainer, PostStartContainer, RemoveContainer,
		} {
			require.Error(t, ExpectStateAt(hook, api.ContainerState_CONTAINER_UNKNOWN), hook)
		}
	})

	t.Run("should refuse a hook the ladder says nothing about", func(t *testing.T) {
		// Not a conformance finding about the runtime: a test asking about a
		// hook with no defined state is a test bug, and answering nil would
		// make it pass silently.
		require.Error(t, ExpectStateAt(RunPodSandbox, api.ContainerState_CONTAINER_RUNNING))
	})
}

func TestExpectShownStrings(t *testing.T) {
	t.Run("should accept a view carrying what the test set", func(t *testing.T) {
		require.NoError(t, ExpectShownStrings("CDI_devices",
			[]string{"v/d=null"}, []string{"v/d=null", "v/d=zero"}))
	})

	t.Run("should reject a value the plugin was never shown", func(t *testing.T) {
		err := ExpectShownStrings("CDI_devices",
			[]string{"v/d=null"}, nil)
		require.ErrorContains(t, err, "v/d=null")
		require.ErrorContains(t, err, "the test set",
			"a fidelity failure must not read as the runtime failing to apply something")
	})
}

func TestExpectShownMap(t *testing.T) {
	t.Run("should reject a sysctl shown with the wrong value", func(t *testing.T) {
		err := ExpectShownMap("sysctl",
			map[string]string{"net.ipv4.ip_forward": "1"},
			map[string]string{"net.ipv4.ip_forward": "0"})
		require.ErrorContains(t, err, "net.ipv4.ip_forward")
	})

	t.Run("should accept entries the runtime added", func(t *testing.T) {
		require.NoError(t, ExpectShownMap("annotations",
			map[string]string{"a": "1"},
			map[string]string{"a": "1", "added": "2"}))
	})
}

func TestExpectShownScalar(t *testing.T) {
	t.Run("should reject a mismatch naming both sides", func(t *testing.T) {
		err := ExpectShownScalar("oom_score_adj", int64(-17), int64(0))
		require.ErrorContains(t, err, "-17")
		require.ErrorContains(t, err, "oom_score_adj")
	})
}
