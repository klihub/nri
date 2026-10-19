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
	"slices"

	"github.com/containerd/nri/pkg/api"
)

// Scope limits a plugin to the workloads of one test run.
//
// A plugin connected to a real runtime is invoked for every workload on the
// node. Without this filter the suite would adjust, and could fail, workloads it
// did not create.
type Scope struct {
	// Namespaces are the pod namespaces this run owns. Empty means nothing is
	// in scope.
	Namespaces []string
	// RunID is the run id in-scope workloads carry.
	RunID string
}

// Matches reports whether a workload with the given pod namespace, labels and
// annotations belongs to this run. It is the single decision procedure behind
// both InScope and the driver's leak guard, so the two can never diverge.
//
// The pod's namespace is the hard guard: it is pod identity metadata, so unlike
// labels and annotations it does not depend on the runtime round-tripping
// anything. Within one of our namespaces the run id separates concurrent runs of
// the suite, but a pod carrying no run id at all is still ours -- the namespace is
// dedicated -- so we accept it rather than going blind if a runtime drops labels
// and annotations.
//
// This is the right rule for deciding what to filter and what to report, and the
// wrong one for deciding what to destroy. See MatchesForRemoval.
func (s *Scope) Matches(namespace string, labels, annotations map[string]string) bool {
	if !s.inNamespace(namespace) {
		return false
	}

	runID := scopeRunID(labels, annotations)

	return runID == "" || runID == s.RunID
}

// MatchesForRemoval reports whether a workload with the given pod namespace,
// labels and annotations may be stopped and removed by this run. It is the
// strictly narrower predicate every destructive operation must use; Matches is
// not, and sharing one predicate between filtering, reporting and destroying is
// what once let the suite wipe pods it never created.
//
// It requires all three: a namespace in scope, a non-empty RunID of our own, and
// the pod carrying exactly that run id. So there are three tiers inside one of
// our own namespaces, and only the third is removable:
//
//  1. A pod with no run id at all was created by something else -- only an
//     nri-test run sets RunIDLabel -- and must never be destroyed, even though
//     Matches deliberately counts it as in scope so the plugin filter and the leak
//     guard do not go blind when a runtime drops labels. Reporting an unrelated
//     pod is noise someone reads; destroying it cannot be undone.
//  2. A pod carrying another run's id must never be destroyed either. The run id
//     is what lets concurrent runs of the suite share a node, and a label cannot
//     distinguish a crashed run's leftover from a live peer's running workload.
//     Reaping by foreign run id would therefore break a live peer at its own
//     Setup. Since Matches excludes a foreign run id, such a pod is invisible to
//     the leak guard too, so it is named in the stale-pod notice instead and left
//     on the node to be removed by hand -- see MatchesOtherRun. Stale-run reaping
//     needs liveness or age as its evidence, not a label, and is not attempted.
//  3. A pod carrying our own run id is ours by construction and is removed.
//
// The RunID guard fails closed, which is the opposite of what Matches does with an
// empty configured run id: Matches then accepts an unlabelled pod in scope, while
// removal with no run id of our own cannot identify anything as ours and so
// destroys nothing at all.
func (s *Scope) MatchesForRemoval(namespace string, labels, annotations map[string]string) bool {
	if !s.inNamespace(namespace) {
		return false
	}

	// Fail closed: with no run id of our own, nothing can be identified as ours.
	if s.RunID == "" {
		return false
	}

	return scopeRunID(labels, annotations) == s.RunID
}

// MatchesOtherRun reports whether a workload with the given pod namespace, labels
// and annotations is in one of our namespaces but belongs to a different nri-test
// run: namespace in scope, a non-empty RunID of our own, and a non-empty run id on
// the pod that is not ours.
//
// It is the third tier of the rule MatchesForRemoval documents, and it exists
// because that tier would otherwise be handled by nothing at all. Such a pod is
// excluded from removal, since a run id cannot distinguish a crashed run from a
// live one, and excluded from the leak guard, since Matches rejects a foreign run
// id -- so before this predicate existed the suite neither acted on it nor said a
// word about it. This is the predicate behind the stale-pod notice, and reporting
// is all it is for: it describes a workload rather than touching it.
func (s *Scope) MatchesOtherRun(namespace string, labels, annotations map[string]string) bool {
	if !s.inNamespace(namespace) {
		return false
	}

	// Fail closed: with no run id of our own, we cannot tell whose a labelled pod
	// is, and calling every labelled pod somebody else's would report our own.
	if s.RunID == "" {
		return false
	}

	runID := scopeRunID(labels, annotations)

	return runID != "" && runID != s.RunID
}

// inNamespace reports whether the given pod namespace is one this run owns. It is
// the guard all three predicates share, and it fails closed in three ways.
func (s *Scope) inNamespace(namespace string) bool {
	if s == nil {
		return false
	}

	// A pod with no namespace at all is never ours, and rejecting it here means a
	// Scope built as Namespaces: []string{""} -- which slices.Contains would
	// otherwise match -- cannot fail open.
	if namespace == "" {
		return false
	}

	// With no namespace configured, nothing is in scope.
	return slices.Contains(s.Namespaces, namespace)
}

// scopeRunID returns the run id a workload carries, labels first and annotations
// as the fallback, or "" if it carries none. Both predicates read it this way, so
// they can never disagree about which of the two a runtime round-tripped.
func scopeRunID(labels, annotations map[string]string) string {
	if runID := labels[RunIDLabel]; runID != "" {
		return runID
	}

	return annotations[RunIDLabel]
}

// InScope reports whether a pod belongs to this run. See Matches for the rule.
func (s *Scope) InScope(pod *api.PodSandbox) bool {
	if pod == nil || s == nil {
		return false
	}

	return s.Matches(pod.GetNamespace(), pod.GetLabels(), pod.GetAnnotations())
}

// Scope returns the scope plugins of this run must be limited to.
func (c *Config) Scope() *Scope {
	return &Scope{
		Namespaces: []string{c.Namespace},
		RunID:      c.RunID,
	}
}
