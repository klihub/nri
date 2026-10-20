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

package cri

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	internalapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

// fakeRuntime is the part of the CRI runtime service the driver's listing and
// cleanup paths use. The embedded interface is nil, so any other method panics
// rather than silently returning a zero value.
type fakeRuntime struct {
	internalapi.RuntimeService

	pods    []*runtimeapi.PodSandbox
	listErr error

	ctrs       []*runtimeapi.Container
	ctrListErr error

	// podFilters and ctrFilters are every filter the listings were handed, so a
	// test can assert what the driver did not ask the runtime to do. See
	// errLabelSelector.
	podFilters []*runtimeapi.PodSandboxFilter
	ctrFilters []*runtimeapi.ContainerFilter

	stopped []string
	removed []string
	stopErr map[string]error
	// createCtrErr is what CreateContainer fails with, by container name.
	createCtrErr map[string]error
	rmErr        map[string]error

	podResourceUpdates []*runtimeapi.UpdatePodSandboxResourcesRequest

	// sandboxHandlers and sandboxConfigs are the runtime handler and the sandbox
	// configuration every RunPodSandbox was asked for, in call order, so a test can
	// assert what the driver told the runtime to run the sandbox with. Both as
	// slices, for the reason podFilters is: the argument is the whole subject of
	// the assertion, and a single field would hide a second call made with
	// something else. The handler matters only paired with the config it arrived
	// with -- a handler reaching the runtime beside some other pod's configuration
	// is not the handler reaching that pod.
	sandboxHandlers []string
	sandboxConfigs  []*runtimeapi.PodSandboxConfig

	// status is what Status answers with, and statusVerbose is every verbose flag
	// it was asked with: handler discovery wants names only, and a verbose status
	// makes the runtime render its whole configuration to answer it.
	status        *runtimeapi.StatusResponse
	statusErr     error
	statusVerbose []bool

	// version is what Version answers with, and versionAPIs is every apiVersion it
	// was asked with: the driver must negotiate none, since what it wants is
	// whatever the runtime reports about itself.
	version     *runtimeapi.VersionResponse
	versionErr  error
	versionAPIs []string

	execStdout   string
	execStderr   string
	execErr      error
	execIDs      []string
	execCmds     [][]string
	execTimeouts []time.Duration

	// ctrStatus and podStatus are what the two status calls answer with, and the
	// verbose slices are every flag they were asked with: the inspector has to
	// ask verbosely or the runtime renders no spec at all, and a caller that
	// forgot would read an empty one rather than fail.
	ctrStatus        *runtimeapi.ContainerStatusResponse
	ctrStatusErr     error
	ctrStatusVerbose []bool

	podStatus        *runtimeapi.PodSandboxStatusResponse
	podStatusErr     error
	podStatusVerbose []bool
}

func (f *fakeRuntime) ContainerStatus(_ context.Context, _ string, verbose bool) (*runtimeapi.ContainerStatusResponse, error) {
	f.ctrStatusVerbose = append(f.ctrStatusVerbose, verbose)

	if f.ctrStatusErr != nil {
		return nil, f.ctrStatusErr
	}

	return f.ctrStatus, nil
}

func (f *fakeRuntime) PodSandboxStatus(_ context.Context, _ string, verbose bool) (*runtimeapi.PodSandboxStatusResponse, error) {
	f.podStatusVerbose = append(f.podStatusVerbose, verbose)

	if f.podStatusErr != nil {
		return nil, f.podStatusErr
	}

	return f.podStatus, nil
}

// ExecSync models the real one: it returns both streams whether the command
// succeeded or not, since a non-zero exit is reported as an error and what the
// command printed is the diagnosis.
func (f *fakeRuntime) ExecSync(_ context.Context, containerID string, cmd []string, timeout time.Duration) ([]byte, []byte, error) {
	f.execIDs = append(f.execIDs, containerID)
	f.execCmds = append(f.execCmds, cmd)
	f.execTimeouts = append(f.execTimeouts, timeout)

	return []byte(f.execStdout), []byte(f.execStderr), f.execErr
}

// errLabelSelector is what a fake listing returns when it is handed a
// server-side label selector.
//
// The suite must filter client-side: a runtime that does not round-trip labels
// makes a selector match nothing, and a leak guard that matches nothing reports no
// leak. That is the fatal direction, and it was previously honoured by convention
// alone -- the fakes discarded their filter argument, so a driver that grew a
// selector left every test green.
//
// A loud error rather than an empty result, deliberately. Returning nothing would
// be indistinguishable from a legitimately empty node, which is precisely the
// silence being guarded against.
var errLabelSelector = errors.New("listing called with a server-side label " +
	"selector: this run's listings must pass a nil filter and apply framework.Scope " +
	"client-side, or a runtime that drops labels makes the leak guard report no leak")

func (f *fakeRuntime) ListPodSandbox(_ context.Context, filter *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error) {
	f.podFilters = append(f.podFilters, filter)

	if len(filter.GetLabelSelector()) > 0 {
		return nil, fmt.Errorf("ListPodSandbox: %w", errLabelSelector)
	}

	if f.listErr != nil {
		return nil, f.listErr
	}

	return f.pods, nil
}

func (f *fakeRuntime) ListContainers(_ context.Context, filter *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	f.ctrFilters = append(f.ctrFilters, filter)

	// ContainerFilter does have a LabelSelector, unlike PodSandboxFilter. It is
	// rejected all the same, and for the same reason.
	if len(filter.GetLabelSelector()) > 0 {
		return nil, fmt.Errorf("ListContainers: %w", errLabelSelector)
	}

	if f.ctrListErr != nil {
		return nil, f.ctrListErr
	}

	return f.ctrs, nil
}

func (f *fakeRuntime) StopPodSandbox(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)

	return f.stopErr[id]
}

func (f *fakeRuntime) RemovePodSandbox(_ context.Context, id string) error {
	f.removed = append(f.removed, id)

	return f.rmErr[id]
}

// RunPodSandbox records the runtime handler it was asked for and names the
// sandbox after the spec, as a real runtime's id is a stand-in for the pod.
func (f *fakeRuntime) RunPodSandbox(_ context.Context, cfg *runtimeapi.PodSandboxConfig, runtimeHandler string) (string, error) {
	f.sandboxConfigs = append(f.sandboxConfigs, cfg)
	f.sandboxHandlers = append(f.sandboxHandlers, runtimeHandler)

	return cfg.GetMetadata().GetName(), nil
}

// CreateContainer names the container after its config and fails for any
// container named in createCtrErr.
func (f *fakeRuntime) CreateContainer(_ context.Context, _ string, cfg *runtimeapi.ContainerConfig, _ *runtimeapi.PodSandboxConfig) (string, error) {
	name := cfg.GetMetadata().GetName()
	if err := f.createCtrErr[name]; err != nil {
		return "", err
	}

	return "ctr-" + name, nil
}

func (f *fakeRuntime) Status(_ context.Context, verbose bool) (*runtimeapi.StatusResponse, error) {
	f.statusVerbose = append(f.statusVerbose, verbose)

	if f.statusErr != nil {
		return nil, f.statusErr
	}

	return f.status, nil
}

// Version models the real one: the runtime's name and its version come back in
// two fields of one response, which the driver has to return in that order.
func (f *fakeRuntime) Version(_ context.Context, apiVersion string) (*runtimeapi.VersionResponse, error) {
	f.versionAPIs = append(f.versionAPIs, apiVersion)

	if f.versionErr != nil {
		return nil, f.versionErr
	}

	return f.version, nil
}

func (f *fakeRuntime) UpdatePodSandboxResources(_ context.Context, req *runtimeapi.UpdatePodSandboxResourcesRequest) (*runtimeapi.UpdatePodSandboxResourcesResponse, error) {
	f.podResourceUpdates = append(f.podResourceUpdates, req)

	return &runtimeapi.UpdatePodSandboxResourcesResponse{}, nil
}

// criPod returns a listed pod sandbox. Note that the namespace lives under
// Metadata while the labels are top-level, which is what the driver's scope
// filter has to get right.
func criPod(id, namespace string, labels, annotations map[string]string) *runtimeapi.PodSandbox {
	return &runtimeapi.PodSandbox{
		Id: id,
		Metadata: &runtimeapi.PodSandboxMetadata{
			Name:      id,
			Namespace: namespace,
		},
		Labels:      labels,
		Annotations: annotations,
	}
}

// criCtr returns a listed container. Unlike a pod sandbox it carries no
// namespace of its own -- only the id of the sandbox it belongs to -- which is
// what listOurContainers' union rule exists to work around.
func criCtr(id, podID string, labels, annotations map[string]string) *runtimeapi.Container {
	return &runtimeapi.Container{
		Id:           id,
		PodSandboxId: podID,
		Metadata: &runtimeapi.ContainerMetadata{
			Name: id,
		},
		Labels:      labels,
		Annotations: annotations,
	}
}

func testDriver(cfg *framework.Config, rt *fakeRuntime) *Driver {
	return &Driver{cfg: cfg, client: &Client{Runtime: rt}}
}

func scopedConfig() *framework.Config {
	return &framework.Config{Namespace: "nri-test", RunID: "run0"}
}

func podIDs(pods []*runtimeapi.PodSandbox) []string {
	ids := make([]string, 0, len(pods))
	for _, pod := range pods {
		ids = append(ids, pod.GetId())
	}

	return ids
}

func TestDriverListOurPods(t *testing.T) {
	ours := map[string]string{framework.RunIDLabel: "run0"}
	theirs := map[string]string{framework.RunIDLabel: "other"}

	t.Run("should include a pod in our namespace carrying our run id", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p0", "nri-test", ours, nil)}}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p0"}, podIDs(pods))
	})

	t.Run("should include a pod in our namespace carrying no run id", func(t *testing.T) {
		// Graceful degradation: the namespace is dedicated, so the pod is ours
		// even if the runtime dropped our labels.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p0", "nri-test", nil, nil)}}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p0"}, podIDs(pods))
	})

	t.Run("should include a pod whose run id is only in an annotation", func(t *testing.T) {
		// The filter used to read labels only, so it disagreed with Scope.InScope
		// about a pod that round-tripped as an annotation.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "nri-test", nil, ours),
			criPod("p1", "nri-test", nil, theirs),
		}}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p0"}, podIDs(pods))
	})

	t.Run("should exclude a foreign namespace even carrying our run id", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "default", ours, nil),
			criPod("p1", "kube-system", nil, nil),
		}}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Empty(t, pods)
	})

	t.Run("should exclude a pod belonging to another run", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p0", "nri-test", theirs, nil)}}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Empty(t, pods)
	})

	t.Run("should fail closed when the configured namespace is empty", func(t *testing.T) {
		// Without the fail-closed check an empty Config.Namespace matched every
		// pod reporting an empty namespace, and Cleanup would have stopped and
		// removed them all.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "", ours, nil),
			criPod("p1", "nri-test", ours, nil),
			{Id: "p2"},
		}}

		pods, err := testDriver(&framework.Config{RunID: "run0"}, rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Empty(t, pods)
	})

	t.Run("should fail closed for a pod with no namespace", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "", ours, nil),
			{Id: "p1", Labels: ours},
		}}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.NoError(t, err)
		require.Empty(t, pods)
	})

	t.Run("should propagate a listing error", func(t *testing.T) {
		boom := errors.New("boom")
		rt := &fakeRuntime{listErr: boom}

		pods, err := testDriver(scopedConfig(), rt).listOurPods(context.Background())
		require.ErrorIs(t, err, boom)
		require.Nil(t, pods)
	})
}

func TestDriverCleanup(t *testing.T) {
	ours := map[string]string{framework.RunIDLabel: "run0"}
	theirs := map[string]string{framework.RunIDLabel: "other"}

	t.Run("should stop and remove a pod carrying our run id", func(t *testing.T) {
		// This subtest used to assert that an unlabelled pod in our namespace --
		// criPod("p1", "nri-test", nil, nil) -- was stopped and removed as well,
		// because Cleanup listed with Scope.Matches, which treats an absent run id
		// as ours. That was a real defect, not a preference: on a node where a pod
		// namespace called nri-test happens to exist, or with
		// -nri-test.namespace=default, Setup destroyed workloads the suite never
		// created before a single case ran. Destruction now goes through
		// Scope.MatchesForRemoval; see the subtest below for the asymmetry.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "nri-test", ours, nil),
			criPod("p2", "default", ours, nil),
		}}

		require.NoError(t, testDriver(scopedConfig(), rt).Cleanup(context.Background()))
		require.Equal(t, []string{"p0"}, rt.stopped)
		require.Equal(t, []string{"p0"}, rt.removed)
	})

	t.Run("should report but never destroy an unlabelled pod in our namespace", func(t *testing.T) {
		// The whole point of splitting the predicates, asserted as one asymmetry so
		// neither half can be "fixed" into agreement with the other. The same pod,
		// the same driver:
		//
		//   - ListRunPods, the leak guard, still reports it. Scope.Matches counts an
		//     absent run id as ours so the guard does not go blind when a runtime
		//     drops labels; over-reporting is noise an operator reads.
		//   - Cleanup must not touch it. Only an nri-test run sets RunIDLabel, so a
		//     pod carrying none was created by something else -- even inside our own
		//     namespace -- and destroying it is unrecoverable.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", nil, nil)}}
		d := testDriver(scopedConfig(), rt)

		ids, err := d.ListRunPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p1"}, ids, "the leak guard must still report it")

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, rt.stopped, "an unlabelled pod must not be stopped")
		require.Empty(t, rt.removed, "an unlabelled pod must not be removed")
	})

	t.Run("should not touch a pod carrying another run's id", func(t *testing.T) {
		// This subtest was first written the other way round, asserting that another
		// run's pod was stopped and removed as a crashed run's leftover. That was
		// wrong: a run id cannot distinguish a crashed run's leftover from a live
		// peer's workload, and the suite's run ids exist precisely so concurrent runs
		// can share a namespace. Reaping by foreign run id destroys a running peer's
		// sandboxes at Setup. Garbage left for someone to sweep is the cheaper
		// failure, so removal now requires our own run id.
		//
		// The pod is not reported either -- Scope.Matches excludes a foreign run id
		// from the leak guard -- which is the accepted cost of that choice.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", theirs, nil)}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, rt.stopped, "another run's pod must not be stopped")
		require.Empty(t, rt.removed, "another run's pod must not be removed")
	})

	t.Run("should remove nothing when no run id is configured", func(t *testing.T) {
		// Fail closed, the opposite direction from Scope.Matches: with no run id of
		// our own we cannot tell which labelled pod is ours, so we destroy none of
		// them rather than all of them.
		cfg := scopedConfig()
		cfg.RunID = ""

		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "nri-test", ours, nil),
			criPod("p1", "nri-test", theirs, nil),
			criPod("p2", "nri-test", nil, nil),
		}}

		require.NoError(t, testDriver(cfg, rt).Cleanup(context.Background()))
		require.Empty(t, rt.stopped)
		require.Empty(t, rt.removed)
	})

	t.Run("should not touch a pod in a foreign namespace whatever it carries", func(t *testing.T) {
		// The namespace is the hard guard, and it outranks any run id: pod identity
		// metadata does not depend on the runtime round-tripping labels.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "default", ours, nil),
			criPod("p1", "default", theirs, nil),
			criPod("p2", "kube-system", nil, nil),
		}}

		require.NoError(t, testDriver(scopedConfig(), rt).Cleanup(context.Background()))
		require.Empty(t, rt.stopped)
		require.Empty(t, rt.removed)
	})

	t.Run("should attempt removal even when the stop fails", func(t *testing.T) {
		// Both runtimes force-stop a sandbox inside RemovePodSandbox, so skipping
		// the removal after a failed stop leaked the pod for no reason.
		rt := &fakeRuntime{
			pods:    []*runtimeapi.PodSandbox{criPod("p0", "nri-test", ours, nil)},
			stopErr: map[string]error{"p0": errors.New("stop failed")},
		}

		err := testDriver(scopedConfig(), rt).Cleanup(context.Background())
		require.ErrorContains(t, err, "failed to stop pod p0")
		require.Equal(t, []string{"p0"}, rt.removed)
	})

	t.Run("should remove the remaining pods after one fails", func(t *testing.T) {
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{
				criPod("p0", "nri-test", ours, nil),
				criPod("p1", "nri-test", ours, nil),
			},
			rmErr: map[string]error{"p0": errors.New("remove failed")},
		}

		err := testDriver(scopedConfig(), rt).Cleanup(context.Background())
		require.ErrorContains(t, err, "failed to remove pod p0")
		require.ElementsMatch(t, []string{"p0", "p1"}, rt.removed)
	})

	t.Run("should remove a tracked pod the listing does not report", func(t *testing.T) {
		// If a runtime under-reports Metadata.Namespace the scope filter rejects
		// our own pod, and only the driver's own record still knows its id.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p0", "", ours, nil)}}
		d := testDriver(scopedConfig(), rt)
		d.pods = []*framework.Pod{{ID: "p0"}}

		require.NoError(t, d.Cleanup(context.Background()))
		require.Equal(t, []string{"p0"}, rt.stopped)
		require.Equal(t, []string{"p0"}, rt.removed)
	})

	t.Run("should clean up the union of tracked and listed pods", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", ours, nil)}}
		d := testDriver(scopedConfig(), rt)
		d.pods = []*framework.Pod{{ID: "p0"}}

		require.NoError(t, d.Cleanup(context.Background()))
		require.ElementsMatch(t, []string{"p0", "p1"}, rt.removed)
	})

	t.Run("should not remove a pod twice when both sources report it", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p0", "nri-test", ours, nil)}}
		d := testDriver(scopedConfig(), rt)
		d.pods = []*framework.Pod{{ID: "p0"}}

		require.NoError(t, d.Cleanup(context.Background()))
		require.Equal(t, []string{"p0"}, rt.stopped)
		require.Equal(t, []string{"p0"}, rt.removed)
	})

	t.Run("should still remove tracked pods when the listing fails", func(t *testing.T) {
		boom := errors.New("boom")
		rt := &fakeRuntime{listErr: boom}
		d := testDriver(scopedConfig(), rt)
		d.pods = []*framework.Pod{{ID: "p0"}}

		err := d.Cleanup(context.Background())
		require.ErrorIs(t, err, boom)
		require.Equal(t, []string{"p0"}, rt.removed)
	})

	t.Run("should forget the pods it tracked", func(t *testing.T) {
		rt := &fakeRuntime{}
		d := testDriver(scopedConfig(), rt)
		d.pods = []*framework.Pod{{ID: "p0"}}

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, d.pods)
	})
}

func TestDriverStalePods(t *testing.T) {
	var (
		ours   = map[string]string{framework.RunIDLabel: "run0"}
		theirs = map[string]string{framework.RunIDLabel: "other"}
	)

	staleIDs := func(pods []framework.StalePod) []string {
		ids := make([]string, 0, len(pods))
		for _, pod := range pods {
			ids = append(ids, pod.ID)
		}

		return ids
	}

	t.Run("should name another run's pod that cleanup refused to remove", func(t *testing.T) {
		// The pair that closes the gap the removal predicate opened: a pod carrying
		// another run's id is destroyed by nothing and reported by nothing else, so
		// without this notice it is invisible. Reporting only -- asserted here as the
		// same subtest, so nobody can turn the notice into a removal and stay green.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", theirs, nil)}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, rt.stopped, "the notice must not stop anything")
		require.Empty(t, rt.removed, "the notice must not remove anything")

		stale := d.StalePods()
		require.Equal(t, []string{"p1"}, staleIDs(stale))
		require.Equal(t, "p1", stale[0].Name, "criPod names a pod after its id")
		require.Equal(t, "other", stale[0].RunID)
	})

	t.Run("should read another run's id from an annotation too", func(t *testing.T) {
		// The reported run id must come from wherever the predicate matched it,
		// labels or annotations: a notice that names the pod but blanks the run id
		// leaves the operator unable to tell which run to wait for.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", nil, theirs)}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))

		stale := d.StalePods()
		require.Equal(t, []string{"p1"}, staleIDs(stale))
		require.Equal(t, "other", stale[0].RunID, "the annotated run id must be reported")
	})

	t.Run("should not name our own pods", func(t *testing.T) {
		// Ours are removed, so naming them as another run's would be a lie.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p0", "nri-test", ours, nil)}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, d.StalePods())
		require.Equal(t, []string{"p0"}, rt.removed)
	})

	t.Run("should not name an unlabelled pod in our namespace", func(t *testing.T) {
		// The leak guard already reports it; a second notice about the same pod
		// trains the reader to skim both.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p2", "nri-test", nil, nil)}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, d.StalePods())

		ids, err := d.ListRunPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p2"}, ids, "it belongs to the leak guard, not the notice")
	})

	t.Run("should not name a pod in a foreign namespace whatever it carries", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "default", ours, nil),
			criPod("p1", "default", theirs, nil),
			criPod("p2", "kube-system", nil, nil),
		}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, d.StalePods())
	})

	t.Run("should report nothing on a clean node", func(t *testing.T) {
		rt := &fakeRuntime{}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		require.Empty(t, d.StalePods())
	})

	t.Run("should report nothing before anything has been listed", func(t *testing.T) {
		// The notice rides along with a listing the driver already makes; it never
		// issues a CRI call of its own, so before the first listing it knows nothing.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", theirs, nil)}}

		require.Empty(t, testDriver(scopedConfig(), rt).StalePods())
		require.Empty(t, rt.podFilters, "StalePods must not list on its own")
	})

	t.Run("should be refreshed by the leak guard's listing too", func(t *testing.T) {
		// Any listing updates it, so the last notice reflects the node as of the most
		// recent look rather than as of Setup.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", theirs, nil)}}
		d := testDriver(scopedConfig(), rt)

		_, err := d.ListRunPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p1"}, staleIDs(d.StalePods()))
	})

	t.Run("should name a pod once however often it is listed", func(t *testing.T) {
		// Each listing replaces the record rather than appending to it. Accumulating
		// would print one pod several times, which reads as several stale pods.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{criPod("p1", "nri-test", theirs, nil)}}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.Cleanup(context.Background()))
		_, err := d.ListRunPods(context.Background())
		require.NoError(t, err)
		_, err = d.ListRunContainers(context.Background())
		require.NoError(t, err)

		require.Equal(t, []string{"p1"}, staleIDs(d.StalePods()))
	})
}

func TestCreatePodReturnsTheContainersItCreated(t *testing.T) {
	// The suite attributes leaked containers to the case that created them, which
	// it can only do for the ones CreatePod tells it about.
	specs := []*framework.ContainerSpec{{Name: "a"}, {Name: "b"}, {Name: "c"}}

	t.Run("lists them in spec order", func(t *testing.T) {
		pod, err := testDriver(scopedConfig(), &fakeRuntime{}).CreatePod(context.Background(),
			&framework.PodSpec{Name: "p0", Namespace: "nri-test", Containers: specs})
		require.NoError(t, err)

		var ids []string
		for _, c := range pod.Containers {
			require.Equal(t, "p0", c.PodID)
			ids = append(ids, c.ID)
		}

		require.Equal(t, []string{"ctr-a", "ctr-b", "ctr-c"}, ids)
	})

	t.Run("lists the ones made before a failure", func(t *testing.T) {
		rt := &fakeRuntime{createCtrErr: map[string]error{"c": errors.New("refused")}}

		pod, err := testDriver(scopedConfig(), rt).CreatePod(context.Background(),
			&framework.PodSpec{Name: "p0", Namespace: "nri-test", Containers: specs})
		require.Error(t, err)
		require.NotNil(t, pod, "the pod exists and must be returned to be cleaned up")
		require.Len(t, pod.Containers, 2)
	})
}

func TestCreatePodPassesRuntimeHandler(t *testing.T) {
	// The whole point of the axis: a feature that fails under crun and passes under
	// runc is a crun defect rather than an NRI one, and nothing can produce that
	// distinction unless the handler the spec names actually reaches the runtime.
	// RunPodSandbox's third argument is the only place CRI takes it.
	t.Run("should ask the runtime for the handler the spec names", func(t *testing.T) {
		rt := &fakeRuntime{}

		pod, err := testDriver(scopedConfig(), rt).CreatePod(context.Background(),
			&framework.PodSpec{Name: "p0", Namespace: "nri-test", RuntimeHandler: "crun"})
		require.NoError(t, err)
		require.Equal(t, "p0", pod.ID)
		require.Equal(t, []string{"crun"}, rt.sandboxHandlers)

		// The handler is only the handler of this pod if it arrived with this pod's
		// configuration, on one call. Asserting the handler alone would pass with
		// the config built from some other spec, or with a second RunPodSandbox the
		// driver made for a sandbox nobody asked for -- and a run whose workloads
		// were not the ones the handler was selected for produces a per-handler
		// result that means nothing.
		require.Len(t, rt.sandboxConfigs, 1, "one CreatePod must run one sandbox")
		require.Equal(t, "p0", rt.sandboxConfigs[0].GetMetadata().GetName())
		require.Equal(t, "nri-test", rt.sandboxConfigs[0].GetMetadata().GetNamespace())
	})

	t.Run("should ask for no handler when the spec names none", func(t *testing.T) {
		// An empty handler means the runtime's default, which is legitimate and is
		// what every run that does not pass -nri-test.runtime-handler gets. It must
		// stay an empty argument: a runtime handed a handler name it does not know
		// refuses the sandbox.
		rt := &fakeRuntime{}

		_, err := testDriver(scopedConfig(), rt).CreatePod(context.Background(),
			&framework.PodSpec{Name: "p0", Namespace: "nri-test"})
		require.NoError(t, err)
		require.Equal(t, []string{""}, rt.sandboxHandlers)
	})
}

func TestRuntimeHandlersDropsTheUnnamedDefault(t *testing.T) {
	// Measured against CRI-O 1.37.2, which reports exactly three handlers: crun,
	// runc, and one with no name key at all, whose GetName() is "". The unnamed
	// entry is the runtime's default, and offering it as selectable would produce a
	// -nri-test.runtime-handler="" that reads as a selection while asking for the
	// default -- the one value that must mean "I selected nothing".
	rt := &fakeRuntime{status: &runtimeapi.StatusResponse{
		RuntimeHandlers: []*runtimeapi.RuntimeHandler{
			{Name: "crun"},
			{Name: "runc"},
			{},
		},
	}}

	handlers, err := testDriver(scopedConfig(), rt).RuntimeHandlers(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"crun", "runc"}, handlers)

	require.Equal(t, []bool{false}, rt.statusVerbose,
		"discovery wants handler names, and a verbose status makes the runtime "+
			"render its whole configuration to answer that")
}

func TestRuntimeHandlersReportsStatusFailure(t *testing.T) {
	// Returned rather than swallowed: Setup makes a discovery failure fatal only
	// when a handler was requested, and it cannot make that distinction if the
	// failure arrives as an empty list indistinguishable from a runtime that
	// advertises nothing.
	boom := errors.New("boom")

	handlers, err := testDriver(scopedConfig(), &fakeRuntime{statusErr: boom}).
		RuntimeHandlers(context.Background())
	require.ErrorIs(t, err, boom)
	require.Nil(t, handlers)
}

func TestDriverUpdatePodResources(t *testing.T) {
	t.Run("should carry the requested resources", func(t *testing.T) {
		rt := &fakeRuntime{}
		d := testDriver(scopedConfig(), rt)

		err := d.UpdatePodResources(context.Background(), &framework.Pod{ID: "p0"},
			&framework.Resources{CPUShares: 512})
		require.NoError(t, err)
		require.Len(t, rt.podResourceUpdates, 1)
		require.Equal(t, int64(512), rt.podResourceUpdates[0].GetResources().GetCpuShares())
	})

	t.Run("should not panic on nil resources", func(t *testing.T) {
		// linuxResources used to dereference its argument, so this call panicked.
		rt := &fakeRuntime{}
		d := testDriver(scopedConfig(), rt)

		require.NoError(t, d.UpdatePodResources(context.Background(), &framework.Pod{ID: "p0"}, nil))
		require.Len(t, rt.podResourceUpdates, 1)
		require.Nil(t, rt.podResourceUpdates[0].GetResources())
	})
}

func TestDriverListRunPods(t *testing.T) {
	t.Run("should return the ids of this run's pods", func(t *testing.T) {
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "nri-test", map[string]string{framework.RunIDLabel: "run0"}, nil),
			criPod("p1", "default", nil, nil),
		}}

		ids, err := testDriver(scopedConfig(), rt).ListRunPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"p0"}, ids)
	})

	t.Run("should propagate a listing error", func(t *testing.T) {
		boom := errors.New("boom")

		ids, err := testDriver(scopedConfig(), &fakeRuntime{listErr: boom}).ListRunPods(context.Background())
		require.ErrorIs(t, err, boom)
		require.Nil(t, ids)
	})

	t.Run("should list with no server-side label selector", func(t *testing.T) {
		// The scope filter is applied client-side on purpose: a runtime that does
		// not round-trip labels would make a selector match nothing, and a leak
		// guard that matches nothing reports no leak. Asserted on the filter the
		// runtime was handed, not merely on the result, so the constraint is pinned
		// rather than left to convention. See errLabelSelector.
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{
			criPod("p0", "nri-test", map[string]string{framework.RunIDLabel: "run0"}, nil),
		}}

		_, err := testDriver(scopedConfig(), rt).ListRunPods(context.Background())
		require.NoError(t, err)

		require.Len(t, rt.podFilters, 1)
		require.Empty(t, rt.podFilters[0].GetLabelSelector(),
			"the pod listing must not filter server-side by label")
	})
}

func TestDriverListRunContainers(t *testing.T) {
	var (
		ours   = map[string]string{framework.RunIDLabel: "run0"}
		theirs = map[string]string{framework.RunIDLabel: "other"}

		// ourPod is this run's; concurrentPod belongs to another run of the suite
		// sharing the namespace; foreignPod has nothing to do with us.
		ourPod        = criPod("p0", "nri-test", ours, nil)
		concurrentPod = criPod("p1", "nri-test", theirs, nil)
		foreignPod    = criPod("p2", "default", nil, nil)
	)

	t.Run("should return a container in a pod of this run", func(t *testing.T) {
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{ourPod},
			ctrs: []*runtimeapi.Container{criCtr("c0", "p0", ours, nil)},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should not return a container of a concurrent run", func(t *testing.T) {
		// Both pods are in our namespace, so only the run id separates them.
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{ourPod, concurrentPod},
			ctrs: []*runtimeapi.Container{
				criCtr("c0", "p0", ours, nil),
				criCtr("c1", "p1", theirs, nil),
			},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should return an unlabelled container in an unlabelled pod of our namespace", func(t *testing.T) {
		// Graceful degradation, the same rule Scope.Matches applies to pods: the
		// namespace is dedicated, so a workload carrying no run id at all is still
		// ours rather than us going blind because the runtime dropped our labels.
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{criPod("p0", "nri-test", nil, nil)},
			ctrs: []*runtimeapi.Container{criCtr("c0", "p0", nil, nil)},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should return an unlabelled container in a pod this driver tracked", func(t *testing.T) {
		// The first arm of the union, and the reason it reuses cleanupIDs rather
		// than the listing alone: a runtime that under-reports Metadata.Namespace
		// leaves our own pod out of scope, and only d.pods still knows it is ours.
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{criPod("p0", "", nil, nil)},
			ctrs: []*runtimeapi.Container{criCtr("c0", "p0", nil, nil)},
		}
		d := testDriver(scopedConfig(), rt)
		d.pods = []*framework.Pod{{ID: "p0"}}

		ids, err := d.ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should not return an unlabelled container in a foreign pod", func(t *testing.T) {
		// The direction that keeps the guard usable on a busy node. Resolving a
		// container's namespace as cfg.Namespace instead of from its pod would
		// report every unlabelled container on the node as this run's leak, and a
		// guard that fires on every run is a guard people learn to ignore.
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{foreignPod},
			ctrs: []*runtimeapi.Container{criCtr("c0", "p2", nil, nil)},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Empty(t, ids)
	})

	t.Run("should return a container carrying our run id in a pod we know nothing about", func(t *testing.T) {
		// The second arm of the union: a container whose pod we somehow missed is
		// still caught by its own label. Over-reporting is a noisy failure someone
		// investigates; missing a leak is the fatal direction.
		rt := &fakeRuntime{
			ctrs: []*runtimeapi.Container{criCtr("c0", "p9", ours, nil)},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should read our run id from a container's annotations too", func(t *testing.T) {
		// Same fallback order Scope.Matches uses: labels first, then annotations.
		rt := &fakeRuntime{
			ctrs: []*runtimeapi.Container{criCtr("c0", "p9", nil, ours)},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should prefer a container's label over its annotation", func(t *testing.T) {
		// Same precedence Scope.Matches applies, in both directions: c0's label says
		// this run and c1's says another, and the annotations say the opposite of
		// each. Reading annotations first would swap the answer for both.
		rt := &fakeRuntime{ctrs: []*runtimeapi.Container{
			criCtr("c0", "p9", ours, theirs),
			criCtr("c1", "p9", theirs, ours),
		}}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"c0"}, ids)
	})

	t.Run("should list with no server-side label selector", func(t *testing.T) {
		// ContainerFilter does have a LabelSelector, unlike PodSandboxFilter -- and it
		// is still not used, for the reason the pod path documents. Asserted on the
		// filter the runtime was handed, so nothing but this test stands between the
		// guard and a selector that silently matches nothing. See errLabelSelector.
		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{ourPod},
			ctrs: []*runtimeapi.Container{criCtr("c0", "p0", ours, nil)},
		}

		_, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.NoError(t, err)

		require.Len(t, rt.ctrFilters, 1)
		require.Empty(t, rt.ctrFilters[0].GetLabelSelector(),
			"the container listing must not filter server-side by label")
	})

	t.Run("should claim nothing by label when no run id is configured", func(t *testing.T) {
		// Fail closed: with an empty run id, matching a container's empty run id
		// would make arm 2 claim every unlabelled container on the node.
		cfg := scopedConfig()
		cfg.RunID = ""

		rt := &fakeRuntime{
			pods: []*runtimeapi.PodSandbox{foreignPod},
			ctrs: []*runtimeapi.Container{criCtr("c0", "p2", nil, nil)},
		}

		ids, err := testDriver(cfg, rt).ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Empty(t, ids)
	})

	t.Run("should propagate a container listing error", func(t *testing.T) {
		// Swallowing it is exactly what made the pod leak guard silently useless.
		boom := errors.New("boom")
		rt := &fakeRuntime{pods: []*runtimeapi.PodSandbox{ourPod}, ctrListErr: boom}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.ErrorIs(t, err, boom)
		require.Nil(t, ids)
	})

	t.Run("should propagate a pod listing error", func(t *testing.T) {
		// The first arm cannot be computed, so the guard says so rather than
		// reporting a container set it knows is incomplete.
		boom := errors.New("boom")
		rt := &fakeRuntime{
			listErr: boom,
			ctrs:    []*runtimeapi.Container{criCtr("c0", "p0", ours, nil)},
		}

		ids, err := testDriver(scopedConfig(), rt).ListRunContainers(context.Background())
		require.ErrorIs(t, err, boom)
		require.Nil(t, ids)
	})
}

func TestDriverProbeCapabilities(t *testing.T) {
	t.Run("should advertise what CRI inherently provides", func(t *testing.T) {
		// Exec among them: ExecSync is a required method of the CRI runtime service.
		// Whether the container's image has anything to run is the image's business,
		// which is why DefaultImage documents that it must have a shell.
		caps := testDriver(scopedConfig(), &fakeRuntime{}).probeCapabilities()

		require.True(t, caps.Has(framework.CapExec), "caps were %s", caps)
		require.True(t, caps.Has(framework.CapContainerLifecycle|
			framework.CapObserveOpError|
			framework.CapUpdateContainerResources), "caps were %s", caps)
	})

	t.Run("should not advertise pod resource updates", func(t *testing.T) {
		// Deliberately never set; see probeCapabilities.
		caps := testDriver(scopedConfig(), &fakeRuntime{}).probeCapabilities()

		require.False(t, caps.Has(framework.CapUpdatePodResources), "caps were %s", caps)
	})

	t.Run("should advertise the CRI-level input capabilities", func(t *testing.T) {
		// CRI takes CDI device names, sysctls and the security context
		// verbatim, so the input fidelity tests can choose their values here.
		// Without these bits every one of those tests skips, and a suite of
		// skips reads to its reader as a suite that measured something.
		caps := testDriver(scopedConfig(), &fakeRuntime{}).probeCapabilities()

		require.True(t, caps.Has(framework.CapCDIDevices|
			framework.CapSysctls|
			framework.CapSecurityContext), "caps were %s", caps)
	})
}

// fakeImages is the part of the CRI image service the driver's pull path uses.
// The embedded interface is nil, so any other method panics rather than silently
// returning a zero value.
type fakeImages struct {
	internalapi.ImageManagerService

	pulled  []string
	pullErr error
}

func (f *fakeImages) PullImage(_ context.Context, image *runtimeapi.ImageSpec, _ *runtimeapi.AuthConfig, _ *runtimeapi.PodSandboxConfig) (string, error) {
	f.pulled = append(f.pulled, image.GetImage())

	if f.pullErr != nil {
		return "", f.pullErr
	}

	return image.GetImage(), nil
}

func TestDriverPullImage(t *testing.T) {
	// Nothing pulled the test image before framework.Setup grew a pull phase, and
	// CRI's CreateContainer does not pull: on a node without the image every
	// container-creating test failed with a create error that named neither the
	// image nor pulling.
	newDriver := func(img *fakeImages) *Driver {
		cfg := scopedConfig()
		cfg.Image = "registry.k8s.io/pause:3.10"

		return &Driver{cfg: cfg, client: &Client{Image: img}}
	}

	t.Run("should pull the named image", func(t *testing.T) {
		img := &fakeImages{}

		require.NoError(t, newDriver(img).PullImage(context.Background(), "busybox:1"))
		require.Equal(t, []string{"busybox:1"}, img.pulled)
	})

	t.Run("should fall back to the configured image", func(t *testing.T) {
		img := &fakeImages{}

		require.NoError(t, newDriver(img).PullImage(context.Background(), ""))
		require.Equal(t, []string{"registry.k8s.io/pause:3.10"}, img.pulled)
	})

	t.Run("should name the image in a pull failure", func(t *testing.T) {
		boom := errors.New("boom")
		img := &fakeImages{pullErr: boom}

		err := newDriver(img).PullImage(context.Background(), "")
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "registry.k8s.io/pause:3.10")
	})
}

// The framework discovers ConflictsWithKubelet by an unexported interface, so
// nothing at compile time ties this method to it. Pin its name and signature
// here: a rename would otherwise silently disable the kubelet refusal.
func TestDriverConflictsWithKubelet(t *testing.T) {
	d := &Driver{}

	require.True(t, d.ConflictsWithKubelet())
}

// Driver must satisfy the interface framework.preflight discovers RuntimeVersion
// by, which is an interface literal in framework/suite.go that nothing here can
// see. This pins the driver's half of it: a rename or a changed signature on this
// side would otherwise cost the suite its provenance with no build error.
//
// It cannot pin the other half, and does not pretend to -- a method renamed on
// both sides still compiles and still matches. What it rules out is the one-sided
// change, which is the whole failure mode of discovery by type assertion.
var _ interface {
	RuntimeVersion(context.Context) (string, string, error)
} = (*Driver)(nil)

func TestDriverRuntimeVersion(t *testing.T) {
	// What a silent mismatch here costs is the header's reason for existing. The
	// assertion stops matching, the suite never learns the runtime's name or
	// version, every test still passes, and the summary prints "runtime:
	// <unknown>" -- so every result the run recorded is unquotable, because
	// nothing in it says what produced it.
	t.Run("should report the runtime's name and version", func(t *testing.T) {
		rt := &fakeRuntime{version: &runtimeapi.VersionResponse{
			RuntimeName:    "cri-o",
			RuntimeVersion: "1.37.2",
		}}

		name, version, err := testDriver(scopedConfig(), rt).RuntimeVersion(context.Background())
		require.NoError(t, err)

		// Asserted as a pair, not as two strings: both are strings of the same
		// type returned side by side, so either assertion on its own passes with
		// the two swapped, and a header reading "runtime: 1.37.2 cri-o" is exactly
		// as unquotable as an empty one.
		require.Equal(t, []string{"cri-o", "1.37.2"}, []string{name, version},
			"the name and the version must come back in that order")

		// CRI's Version negotiates an API version. The driver asks for none,
		// deliberately: what it wants is whatever the runtime reports about itself,
		// and naming a version would let a runtime refuse to answer at all.
		require.Equal(t, []string{""}, rt.versionAPIs,
			"the driver must not negotiate an API version to read the runtime's own")
	})

	t.Run("should report a refusal as an error, not as an unknown runtime", func(t *testing.T) {
		// The error has to propagate: preflight returns it, which fails Setup
		// loudly. Swallowing it would leave the run going with an empty runtime
		// name, which is the unquotable state this whole method exists to avoid.
		boom := errors.New("connection refused")
		rt := &fakeRuntime{versionErr: boom}

		name, version, err := testDriver(scopedConfig(), rt).RuntimeVersion(context.Background())
		require.ErrorIs(t, err, boom)
		require.Empty(t, name)
		require.Empty(t, version)
	})
}
