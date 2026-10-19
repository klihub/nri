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

// scopedPod returns a pod in the given namespace, labelled with runID when one
// is given.
func scopedPod(id, namespace, runID string) *api.PodSandbox {
	pod := testPod(id)
	pod.Namespace = namespace
	if runID != "" {
		pod.Labels = map[string]string{RunIDLabel: runID}
	}

	return pod
}

func TestScopeMatches(t *testing.T) {
	var (
		ours    = map[string]string{RunIDLabel: "run0"}
		theirs  = map[string]string{RunIDLabel: "other"}
		noRunID = map[string]string{CaseLabel: "case0"}
	)

	for _, tc := range []struct {
		name        string
		scope       *Scope
		namespace   string
		labels      map[string]string
		annotations map[string]string
		want        bool
	}{
		{
			name:      "should accept our namespace with our run id in a label",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "nri-test",
			labels:    ours,
			want:      true,
		},
		{
			name:        "should accept our namespace with our run id in an annotation",
			scope:       &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace:   "nri-test",
			labels:      noRunID,
			annotations: ours,
			want:        true,
		},
		{
			name:      "should accept our namespace with no run id at all",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "nri-test",
			want:      true,
		},
		{
			name:      "should reject our namespace with another run's id",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "nri-test",
			labels:    theirs,
			want:      false,
		},
		{
			name:        "should reject our namespace with another run's id in an annotation",
			scope:       &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace:   "nri-test",
			annotations: theirs,
			want:        false,
		},
		{
			name:      "should reject a foreign namespace even carrying our run id",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "default",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should reject a foreign namespace carrying no run id",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "kube-system",
			want:      false,
		},
		{
			name:      "should accept any of several namespaces",
			scope:     &Scope{Namespaces: []string{"nri-test", "nri-test-2"}, RunID: "run0"},
			namespace: "nri-test-2",
			labels:    ours,
			want:      true,
		},
		{
			name:      "should fail closed on an empty namespace",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed with no namespaces configured",
			scope:     &Scope{RunID: "run0"},
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed on an empty namespace against an empty configured namespace",
			scope:     &Scope{Namespaces: []string{""}, RunID: "run0"},
			namespace: "",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed for a nil scope",
			scope:     nil,
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should accept nil label and annotation maps in our namespace",
			scope:     &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"},
			namespace: "nri-test",
			want:      true,
		},
		{
			name:      "should reject everything when the scope has an empty run id and the pod has one",
			scope:     &Scope{Namespaces: []string{"nri-test"}},
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.scope.Matches(tc.namespace, tc.labels, tc.annotations))
		})
	}
}

func TestScopeMatchesForRemoval(t *testing.T) {
	var (
		ours    = map[string]string{RunIDLabel: "run0"}
		theirs  = map[string]string{RunIDLabel: "other"}
		noRunID = map[string]string{CaseLabel: "case0"}

		scope = &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"}
	)

	for _, tc := range []struct {
		name        string
		scope       *Scope
		namespace   string
		labels      map[string]string
		annotations map[string]string
		want        bool
	}{
		{
			name:      "should accept our namespace with our run id in a label",
			scope:     scope,
			namespace: "nri-test",
			labels:    ours,
			want:      true,
		},
		{
			name:        "should accept our namespace with our run id in an annotation",
			scope:       scope,
			namespace:   "nri-test",
			labels:      noRunID,
			annotations: ours,
			want:        true,
		},
		{
			name:      "should reject our namespace with another run's id",
			scope:     scope,
			namespace: "nri-test",
			labels:    theirs,
			want:      false,
		},
		{
			name:        "should reject our namespace with another run's id in an annotation",
			scope:       scope,
			namespace:   "nri-test",
			annotations: theirs,
			want:        false,
		},
		{
			name:      "should reject our namespace with no run id at all",
			scope:     scope,
			namespace: "nri-test",
			want:      false,
		},
		{
			name:      "should reject our namespace with a run-id label present but empty",
			scope:     scope,
			namespace: "nri-test",
			labels:    map[string]string{RunIDLabel: ""},
			want:      false,
		},
		{
			name:      "should reject our namespace with labels carrying no run id",
			scope:     scope,
			namespace: "nri-test",
			labels:    noRunID,
			want:      false,
		},
		{
			name:      "should reject a foreign namespace even carrying our run id",
			scope:     scope,
			namespace: "default",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should reject a foreign namespace carrying no run id",
			scope:     scope,
			namespace: "kube-system",
			want:      false,
		},
		{
			name:      "should accept any of several namespaces",
			scope:     &Scope{Namespaces: []string{"nri-test", "nri-test-2"}, RunID: "run0"},
			namespace: "nri-test-2",
			labels:    ours,
			want:      true,
		},
		{
			name:      "should fail closed on an empty namespace",
			scope:     scope,
			namespace: "",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed with no namespaces configured",
			scope:     &Scope{RunID: "run0"},
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed on an empty namespace against an empty configured namespace",
			scope:     &Scope{Namespaces: []string{""}, RunID: "run0"},
			namespace: "",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed for a nil scope",
			scope:     nil,
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
		{
			// The opposite direction from Matches, deliberately: an empty configured
			// run id makes Matches permissive about an unlabelled pod, and must make
			// removal destroy nothing at all.
			name:      "should fail closed with no run id configured",
			scope:     &Scope{Namespaces: []string{"nri-test"}},
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed with no run id configured and none on the pod",
			scope:     &Scope{Namespaces: []string{"nri-test"}},
			namespace: "nri-test",
			want:      false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want,
				tc.scope.MatchesForRemoval(tc.namespace, tc.labels, tc.annotations))
		})
	}
}

func TestScopeMatchesOtherRun(t *testing.T) {
	var (
		ours    = map[string]string{RunIDLabel: "run0"}
		theirs  = map[string]string{RunIDLabel: "other"}
		noRunID = map[string]string{CaseLabel: "case0"}

		scope = &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"}
	)

	for _, tc := range []struct {
		name        string
		scope       *Scope
		namespace   string
		labels      map[string]string
		annotations map[string]string
		want        bool
	}{
		{
			name:      "should accept another run's pod in our namespace",
			scope:     scope,
			namespace: "nri-test",
			labels:    theirs,
			want:      true,
		},
		{
			name:        "should accept another run's id carried in an annotation",
			scope:       scope,
			namespace:   "nri-test",
			labels:      noRunID,
			annotations: theirs,
			want:        true,
		},
		{
			// Ours is removed, so reporting it as somebody else's would be a lie.
			name:      "should reject a pod of this run",
			scope:     scope,
			namespace: "nri-test",
			labels:    ours,
			want:      false,
		},
		{
			// The leak guard already reports it; two notices about one pod teach the
			// reader to skim both.
			name:      "should reject an unlabelled pod in our namespace",
			scope:     scope,
			namespace: "nri-test",
			want:      false,
		},
		{
			name:      "should reject a foreign namespace carrying another run's id",
			scope:     scope,
			namespace: "default",
			labels:    theirs,
			want:      false,
		},
		{
			name:      "should reject a foreign namespace carrying our run id",
			scope:     scope,
			namespace: "default",
			labels:    ours,
			want:      false,
		},
		{
			name:      "should fail closed on an empty namespace",
			scope:     scope,
			namespace: "",
			labels:    theirs,
			want:      false,
		},
		{
			name:      "should fail closed with no namespaces configured",
			scope:     &Scope{RunID: "run0"},
			namespace: "nri-test",
			labels:    theirs,
			want:      false,
		},
		{
			// With no run id of our own we cannot say whose a labelled pod is, and
			// claiming every labelled pod is somebody else's would report our own.
			name:      "should fail closed with no run id configured",
			scope:     &Scope{Namespaces: []string{"nri-test"}},
			namespace: "nri-test",
			labels:    theirs,
			want:      false,
		},
		{
			name:      "should fail closed for a nil scope",
			scope:     nil,
			namespace: "nri-test",
			labels:    theirs,
			want:      false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want,
				tc.scope.MatchesOtherRun(tc.namespace, tc.labels, tc.annotations))
		})
	}
}

// TestScopeRemovalIsNotMatches states the three tiers inside one namespace side
// by side, so no predicate can be quietly replaced by another. Every row asserts
// all three, and each pod is handled by exactly one mechanism: our own pod is
// removed, an unlabelled one is reported by the leak guard, another run's is named
// in the stale-pod notice.
//
// The interesting row is the unlabelled pod: in scope, so the plugin filter and
// the leak guard both see it, and never removable, which is the defect the split
// fixes. Another run's pod is out of scope and unremovable -- we cannot tell a
// crashed peer from a live one -- which is why it needs a notice of its own rather
// than silence.
func TestScopeRemovalIsNotMatches(t *testing.T) {
	var (
		scope  = &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"}
		ours   = map[string]string{RunIDLabel: "run0"}
		theirs = map[string]string{RunIDLabel: "other"}
	)

	t.Run("should scope and remove a pod of this run, and not call it stale", func(t *testing.T) {
		require.True(t, scope.Matches("nri-test", ours, nil))
		require.True(t, scope.MatchesForRemoval("nri-test", ours, nil))
		require.False(t, scope.MatchesOtherRun("nri-test", ours, nil))
	})

	t.Run("should keep an unlabelled pod in scope but never remove or rename it", func(t *testing.T) {
		require.True(t, scope.Matches("nri-test", nil, nil))
		require.False(t, scope.MatchesForRemoval("nri-test", nil, nil))
		require.False(t, scope.MatchesOtherRun("nri-test", nil, nil))
	})

	t.Run("should neither scope nor remove another run's pod, but report it", func(t *testing.T) {
		require.False(t, scope.Matches("nri-test", theirs, nil))
		require.False(t, scope.MatchesForRemoval("nri-test", theirs, nil))
		require.True(t, scope.MatchesOtherRun("nri-test", theirs, nil))
	})
}

func TestScopeInScope(t *testing.T) {
	s := &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"}

	t.Run("should accept a pod in our namespace with our run id", func(t *testing.T) {
		require.True(t, s.InScope(scopedPod("pod0", "nri-test", "run0")))
	})

	t.Run("should reject a pod in another namespace even with our run id", func(t *testing.T) {
		// The namespace is the hard guard. A foreign workload that happens to
		// carry our run id must still be untouchable.
		require.False(t, s.InScope(scopedPod("pod0", "default", "run0")))
	})

	t.Run("should reject a pod in another namespace", func(t *testing.T) {
		require.False(t, s.InScope(scopedPod("pod0", "kube-system", "")))
	})

	t.Run("should accept a pod in our namespace carrying no run id", func(t *testing.T) {
		// Degrades gracefully if a runtime drops labels and annotations: the
		// namespace is dedicated, so the pod is ours by construction.
		require.True(t, s.InScope(scopedPod("pod0", "nri-test", "")))
	})

	t.Run("should reject a pod in our namespace with another run id", func(t *testing.T) {
		require.False(t, s.InScope(scopedPod("pod0", "nri-test", "other")))
	})

	t.Run("should accept a pod annotated rather than labelled", func(t *testing.T) {
		pod := scopedPod("pod0", "nri-test", "")
		pod.Annotations = map[string]string{RunIDLabel: "run0"}
		require.True(t, s.InScope(pod))
	})

	t.Run("should reject a nil pod", func(t *testing.T) {
		require.False(t, s.InScope(nil))
	})

	t.Run("should fail closed with no namespace configured", func(t *testing.T) {
		empty := &Scope{RunID: "run0"}
		require.False(t, empty.InScope(scopedPod("pod0", "nri-test", "run0")))
	})

	t.Run("should accept a pod in any of several namespaces", func(t *testing.T) {
		multi := &Scope{Namespaces: []string{"nri-test", "nri-test-2"}, RunID: "run0"}
		require.True(t, multi.InScope(scopedPod("pod0", "nri-test", "run0")))
		require.True(t, multi.InScope(scopedPod("pod1", "nri-test-2", "run0")))
		require.False(t, multi.InScope(scopedPod("pod2", "nri-test-3", "run0")))
	})

	t.Run("should reject a pod with an empty namespace", func(t *testing.T) {
		require.False(t, s.InScope(scopedPod("pod0", "", "run0")))
	})

	t.Run("should not match empty namespace against empty namespace string", func(t *testing.T) {
		// Scope{Namespaces: []string{""}} would otherwise match any pod with an
		// empty namespace due to slices.Contains, but the explicit empty check
		// prevents that fail-open.
		empty := &Scope{Namespaces: []string{""}, RunID: "run0"}
		require.False(t, empty.InScope(scopedPod("pod0", "", "run0")))
	})

	t.Run("should put nothing in scope when Config has empty namespace", func(t *testing.T) {
		// Scope constructed from an empty Config.Namespace should accept nothing.
		c := &Config{}
		scope := c.Scope()
		require.False(t, scope.InScope(scopedPod("pod0", "", "run0")))
		require.False(t, scope.InScope(scopedPod("pod1", "nri-test", "run0")))
	})
}
