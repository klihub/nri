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
	"maps"
	"slices"

	"github.com/containerd/nri/pkg/api"
)

// Comparisons for the input conformance tests: what a plugin was shown against
// what the runtime realized.
//
// Pure functions returning an error, as the expectations in expect.go are, so
// that each is unit testable without a runtime and so that Suite.expect decides
// alone when a failure stops a case. The error text names the field and both
// sides, because a finding is read in a summary long after the test output it
// came from scrolled past.
//
// The comparison rule is subset, not equality: NRI projects the OCI spec rather
// than copying it, and a runtime adds defaults of its own -- devices,
// namespaces, rlimits -- some of them after CreateContainer. Equality would
// fail every runtime on things that are not defects.
//
// Subset alone is not enough, and the gap is the point of ExpectPopulated: it
// catches a runtime fabricating a value, not one omitting it, because the empty
// set is a subset of anything. Omission is the defect class this suite was
// built for, so an agreement check is a subset check AND a non-vacuity guard.

// missingStrings returns the values of want that from does not contain.
//
// The counting behind both the agreement and the fidelity check, shared rather
// than written twice: two copies could drift apart, and the looser one would
// then admit a comparison the other would have caught.
func missingStrings(want, from []string) []string {
	var missing []string

	for _, w := range want {
		if !slices.Contains(from, w) {
			missing = append(missing, w)
		}
	}

	return missing
}

// wrongMapEntries returns a description of every entry of want that from does
// not carry with the same value. Shared for the reason missingStrings is.
func wrongMapEntries(want, from map[string]string) []string {
	var wrong []string

	for _, k := range slices.Sorted(maps.Keys(want)) {
		got, ok := from[k]
		if !ok {
			wrong = append(wrong, fmt.Sprintf("%s=%q is absent", k, want[k]))

			continue
		}

		if got != want[k] {
			wrong = append(wrong, fmt.Sprintf("%s is %q, wanted %q", k, got, want[k]))
		}
	}

	return wrong
}

// ExpectStringsSubset checks that every value the plugin was shown appears in
// what the runtime realized.
//
// Every missing value is named, not only the first: a reader of the summary
// fixes what the message lists, and a message that stopped at the first would
// send them back for the second.
func ExpectStringsSubset(field string, shown, realized []string) error {
	missing := missingStrings(shown, realized)
	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("%s: the plugin was shown %v, which the runtime did not "+
		"realize; it was shown %v and the container has %v",
		field, missing, shown, realized)
}

// The fidelity checks. Separate from the agreement ones because the question
// is the other way round -- the test chose the value, so what is in doubt is
// whether the runtime reported it, not whether it applied it -- and because a
// finding has to say which it was. Reusing the agreement functions with the
// arguments swapped would produce a message claiming the runtime failed to
// realize a value the test never asked it to realize.

// ExpectShownStrings checks that the plugin was shown every value the test set.
func ExpectShownStrings(field string, want, shown []string) error {
	missing := missingStrings(want, shown)
	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("%s: the test set %v, which the plugin was not shown; "+
		"the plugin was shown %v", field, missing, shown)
}

// ExpectShownMap checks that the plugin was shown every entry the test set,
// with the value it set.
func ExpectShownMap(field string, want, shown map[string]string) error {
	wrong := wrongMapEntries(want, shown)
	if len(wrong) == 0 {
		return nil
	}

	return fmt.Errorf("%s: the test set %v; the plugin was shown %v",
		field, wrong, slices.Sorted(maps.Keys(shown)))
}

// ExpectShownScalar checks that the plugin was shown the value the test set.
func ExpectShownScalar[T comparable](field string, want, shown T) error {
	if want == shown {
		return nil
	}

	return fmt.Errorf("%s: the test set %v, the plugin was shown %v",
		field, want, shown)
}

// ExpectMapSubset checks that every key the plugin was shown is realized with
// the same value.
//
// A present key with a different value is not a subset. Checking only that the
// key exists is the version of this that cannot fail.
func ExpectMapSubset(field string, shown, realized map[string]string) error {
	wrong := wrongMapEntries(shown, realized)
	if len(wrong) == 0 {
		return nil
	}

	return fmt.Errorf("%s: %v; the container has %v",
		field, wrong, slices.Sorted(maps.Keys(realized)))
}

// ExpectScalarEqual checks that a single value agrees. Subset degenerates to
// equality for a scalar, so there is nothing weaker to ask of one.
func ExpectScalarEqual[T comparable](field string, shown, realized T) error {
	if shown == realized {
		return nil
	}

	return fmt.Errorf("%s: the plugin was shown %v, the container has %v",
		field, shown, realized)
}

// ExpectPopulated checks that the plugin was shown something, whenever the
// runtime realized something.
//
// The guard without which an agreement check cannot fail. A runtime that
// populates nothing at all passes every subset check in this file, since the
// empty set is a subset of anything -- and a runtime that populates nothing is
// exactly the finding this suite exists to make. CRI-O dropping CRI-injected
// CDI devices is that finding.
//
// A view carrying more than the runtime realized is not this function's
// question. That is fabrication, and the subset checks above are for it.
//
// Where a field may legitimately be empty on a correct runtime, the caller
// omits this check and says at the call site why. The omission is a claim that
// has to be argued, not a default.
func ExpectPopulated(field string, shownLen, realizedLen int) error {
	if shownLen > 0 || realizedLen == 0 {
		return nil
	}

	return fmt.Errorf("%s: the plugin was shown nothing, but the container has "+
		"%d; a field the runtime realized and did not report is an input the "+
		"protocol defines and this runtime does not deliver",
		field, realizedLen)
}

// containerStateLadder is the state a container is in at each hook that has a
// defined answer.
//
// StartContainer is a request hook -- it fires before the container runs -- so
// it sees a created container, not a running one. The hooks absent from here
// have no defined answer rather than a free one; see ExpectStateAt.
var containerStateLadder = map[EventType]api.ContainerState{
	PostCreateContainer: api.ContainerState_CONTAINER_CREATED,
	StartContainer:      api.ContainerState_CONTAINER_CREATED,
	PostStartContainer:  api.ContainerState_CONTAINER_RUNNING,
	StopContainer:       api.ContainerState_CONTAINER_RUNNING,
	RemoveContainer:     api.ContainerState_CONTAINER_STOPPED,
}

// ExpectStateAt checks that a container's reported state is the one its hook
// implies.
//
// Container.state is populated at every hook, so a presence check on it proves
// nothing; what carries information is whether the value tracks the lifecycle.
// CONTAINER_UNKNOWN is the enum's zero value, which is what a runtime that
// never set the field reports, so the ladder is also the check that it was set
// at all.
//
// A hook the ladder says nothing about is an error rather than a pass. Such a
// call is a test bug -- CreateContainer, for one, describes a container that
// does not exist yet -- and answering nil would let it assert nothing.
func ExpectStateAt(hook EventType, got api.ContainerState) error {
	want, ok := containerStateLadder[hook]
	if !ok {
		return fmt.Errorf("state: %s has no state this suite can require; "+
			"asking about it is a test bug rather than a finding about the runtime",
			hook)
	}

	if got == want {
		return nil
	}

	return fmt.Errorf("state: at %s the plugin was shown %s, wanted %s",
		hook, got, want)
}
