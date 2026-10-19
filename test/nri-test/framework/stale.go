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
	"io"
	"strings"
)

// StalePod describes a pod found in one of the run's namespaces that carries a
// different nri-test run's id.
//
// It is a report, never a work item. Scope.MatchesForRemoval deliberately refuses
// to destroy such a pod -- a run id cannot distinguish a crashed run's leftovers
// from a live run's workloads -- and Scope.Matches keeps it out of the leak guard,
// so this description is the only thing the suite has to say about it. Every field
// is here because an operator acting by hand needs it.
type StalePod struct {
	// ID is the pod's id, which is what removing it by hand takes.
	ID string
	// Name is the pod's name, or "" if the listing carried none.
	Name string
	// RunID is the run id the pod carries: never empty, and never this run's.
	RunID string
}

// stalePodLister is the optional driver surface behind the stale-pod notice.
//
// Optional, and discovered by type assertion the way preflight finds a versioner
// and pullImage an imagePuller, because the notice describes workloads the suite
// refuses to touch: a driver that cannot enumerate other runs' pods is still a
// legal driver. That is the opposite of Driver.ListRunPods, which certifies this
// run's own cleanliness and so must not be silently opt-out-able.
//
// It takes no context and does no I/O of its own: a driver answers from what its
// last listing saw, so the notice costs no extra call to the runtime.
type stalePodLister interface {
	StalePods() []StalePod
}

// reportStalePods prints the notice for the pods of other runs the driver saw, and
// prints nothing at all when there are none.
//
// Reporting only, and deliberately so. This is NOT the "reap other runs' pods"
// flag that was refused: that would have destroyed a live peer's workload, this
// only describes one, which is a different risk class entirely -- and unlike the
// flag it has a real caller. Do not "simplify" it into a removal: the pods it
// names are exactly the ones the suite has decided it cannot prove are dead.
//
// Nothing here can fail the run. It returns no error, reports through neither
// testing.T nor the event queue, and so cannot reach the leak guard's verdict or
// the dropped-update guard: a stale peer is information about the node, not a
// fault of this run.
func reportStalePods(w io.Writer, d Driver, namespace string) {
	lister, ok := d.(stalePodLister)
	if !ok {
		return
	}

	if report := staleReport(namespace, lister.StalePods()); report != "" {
		fmt.Fprint(w, report)
	}
}

// staleReport formats the notice, or returns "" when there is nothing to say.
//
// Every line carries the WARNING prefix the suite's other operator-facing notices
// use, so the block reads as one message wherever test output puts it. Silence on
// an empty list is a requirement, not an optimisation: a clean node is the
// overwhelmingly common case, and a notice that prints on every run is one nobody
// reads by the third run.
func staleReport(namespace string, pods []StalePod) string {
	if len(pods) == 0 {
		return ""
	}

	noun, verb := "pods", "belong"
	if len(pods) == 1 {
		noun, verb = "pod", "belongs"
	}

	var b strings.Builder

	fmt.Fprintf(&b, "WARNING: %d %s in namespace %s %s to another nri-test run:\n",
		len(pods), noun, namespace, verb)

	for _, pod := range pods {
		name := pod.Name
		if name == "" {
			name = "<unnamed>"
		}

		fmt.Fprintf(&b, "WARNING:   %s id %s, run id %s\n", name, pod.ID, pod.RunID)
	}

	// Say why we are not acting, so the notice does not read as a bug in cleanup,
	// and say what makes acting safe, so it is actionable rather than merely true.
	b.WriteString("" +
		"WARNING: This run will not remove another run's pods: a run id cannot tell a\n" +
		"WARNING: crashed run's leftovers from a live run's workloads, and removing a\n" +
		"WARNING: live run's pods would break it. Remove them by hand once no other\n" +
		"WARNING: nri-test run is active.\n")

	return b.String()
}
