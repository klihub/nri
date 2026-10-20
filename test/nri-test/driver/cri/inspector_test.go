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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

// containerdInfo is the shape containerd's ContainerStatus verbose info has.
const containerdInfo = `{
  "sandboxID": "abc123",
  "pid": 4242,
  "removing": false,
  "snapshotKey": "abc123",
  "snapshotter": "overlayfs",
  "runtimeType": "io.containerd.runc.v2",
  "config": {},
  "runtimeSpec": {
    "ociVersion": "1.2.0",
    "process": {
      "args": ["/pause"],
      "env": ["PATH=/usr/bin", "INJECTED=yes"]
    },
    "annotations": {
      "nri-test.containerd.io/run-id": "run0"
    },
    "linux": {
      "cgroupsPath": "/kubepods/podabc/ctr0",
      "resources": {
        "cpu": {"shares": 1024}
      }
    }
  }
}`

// crioInfo is the shape CRI-O's ContainerStatus verbose info has.
const crioInfo = `{
  "sandboxID": "def456",
  "pid": 5353,
  "privileged": false,
  "runtimeSpec": {
    "ociVersion": "1.2.0",
    "process": {
      "args": ["/pause"],
      "env": ["PATH=/usr/bin"]
    },
    "linux": {
      "cgroupsPath": "pod_123:crio:ctr0"
    }
  }
}`

func TestParseVerboseInfo(t *testing.T) {
	t.Run("should decode a containerd runtime spec", func(t *testing.T) {
		spec, err := ParseVerboseInfo(map[string]string{"info": containerdInfo})
		require.NoError(t, err)
		require.NotNil(t, spec)
		require.Equal(t, []string{"/pause"}, spec.Process.Args)
		require.Contains(t, spec.Process.Env, "INJECTED=yes")
		require.Equal(t, "run0", spec.Annotations["nri-test.containerd.io/run-id"])
		require.Equal(t, "/kubepods/podabc/ctr0", spec.Linux.CgroupsPath)
		require.NotNil(t, spec.Linux.Resources.CPU.Shares)
		require.Equal(t, uint64(1024), *spec.Linux.Resources.CPU.Shares)
	})

	t.Run("should decode a CRI-O runtime spec", func(t *testing.T) {
		spec, err := ParseVerboseInfo(map[string]string{"info": crioInfo})
		require.NoError(t, err)
		require.NotNil(t, spec)
		require.Equal(t, "pod_123:crio:ctr0", spec.Linux.CgroupsPath)
	})

	t.Run("should fail when the info key is missing", func(t *testing.T) {
		_, err := ParseVerboseInfo(map[string]string{"other": "{}"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "info")
	})

	t.Run("should fail when the info is not JSON", func(t *testing.T) {
		_, err := ParseVerboseInfo(map[string]string{"info": "not json"})
		require.Error(t, err)
	})

	t.Run("should fail when there is no runtime spec", func(t *testing.T) {
		_, err := ParseVerboseInfo(map[string]string{"info": `{"pid": 1}`})
		require.Error(t, err)
		require.Contains(t, err.Error(), "runtimeSpec")
	})
}

func TestContainerState(t *testing.T) {
	t.Run("should report a nil status as unknown", func(t *testing.T) {
		// GetState() on a nil status yields the enum's zero value,
		// CONTAINER_CREATED, so a response missing its status used to be reported
		// as a created container.
		require.Equal(t, framework.StateUnknown, containerState(nil))
	})

	t.Run("should map the CRI states", func(t *testing.T) {
		for state, want := range map[runtimeapi.ContainerState]framework.ContainerState{
			runtimeapi.ContainerState_CONTAINER_CREATED: framework.StateCreated,
			runtimeapi.ContainerState_CONTAINER_RUNNING: framework.StateRunning,
			runtimeapi.ContainerState_CONTAINER_EXITED:  framework.StateExited,
			runtimeapi.ContainerState_CONTAINER_UNKNOWN: framework.StateUnknown,
		} {
			require.Equal(t, want,
				containerState(&runtimeapi.ContainerStatus{State: state}), state.String())
		}
	})
}

func TestInspectorExec(t *testing.T) {
	const status = "Name:\tsleep\nCpus_allowed_list:\t0-1\n"

	newInspector := func(rt *fakeRuntime) *Inspector {
		return NewInspector(&Client{Runtime: rt})
	}

	t.Run("should return the command's standard output", func(t *testing.T) {
		rt := &fakeRuntime{execStdout: status}
		i := newInspector(rt)

		stdout, stderr, err := i.Exec(context.Background(), "ctr0",
			[]string{"cat", "/proc/self/status"})
		require.NoError(t, err)
		require.Equal(t, status, stdout)
		require.Empty(t, stderr)
		require.Equal(t, []string{"ctr0"}, rt.execIDs)
		require.Equal(t, [][]string{{"cat", "/proc/self/status"}}, rt.execCmds)
	})

	t.Run("should take the command's timeout from the context's deadline", func(t *testing.T) {
		// So that the runtime's limit on the command and the caller's limit on the
		// RPC are the same number, rather than two that can disagree.
		rt := &fakeRuntime{}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, _, err := newInspector(rt).Exec(ctx, "ctr0", []string{"true"})
		require.NoError(t, err)
		require.Len(t, rt.execTimeouts, 1)
		require.Positive(t, rt.execTimeouts[0])
		require.LessOrEqual(t, rt.execTimeouts[0], 30*time.Second)
	})

	t.Run("should pass no timeout when the context has no deadline", func(t *testing.T) {
		// CRI reads zero as no timeout, which is the only honest reading of a caller
		// that installed no deadline.
		rt := &fakeRuntime{}

		_, _, err := newInspector(rt).Exec(context.Background(), "ctr0", []string{"true"})
		require.NoError(t, err)
		require.Equal(t, []time.Duration{0}, rt.execTimeouts)
	})

	t.Run("should return both streams alongside an error", func(t *testing.T) {
		// ExecSync reports a non-zero exit as an error and still returns what the
		// command wrote; stderr is where a shell says why it failed.
		boom := errors.New("command 'cat /nope' exited with 1")
		rt := &fakeRuntime{
			execStdout: "partial",
			execStderr: "cat: /nope: No such file or directory",
			execErr:    boom,
		}

		stdout, stderr, err := newInspector(rt).Exec(context.Background(), "ctr0",
			[]string{"cat", "/nope"})
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "ctr0", "the error must name the container")
		require.Equal(t, "partial", stdout)
		require.Equal(t, "cat: /nope: No such file or directory", stderr)
	})
}

// containerdSandboxInfo is the shape containerd's PodSandboxStatus verbose info
// has: a pid and the sandbox's own OCI spec.
const containerdSandboxInfo = `{
  "pid": 7171,
  "runtimeHandler": "runc",
  "config": {},
  "runtimeSpec": {
    "ociVersion": "1.2.0",
    "linux": {
      "cgroupsPath": "/kubepods.slice/kubepods-pod123.slice",
      "namespaces": [{"type": "pid"}, {"type": "ipc"}],
      "resources": {"memory": {"limit": 209715200}}
    }
  }
}`

// crioSandboxInfo is CRI-O's shape, which carries the spec and no pid at all.
const crioSandboxInfo = `{
  "runtimeSpec": {
    "ociVersion": "1.2.0",
    "linux": {
      "cgroupsPath": "pod_123.slice:crio:infra",
      "namespaces": [{"type": "pid"}],
      "resources": {"cpu": {"shares": 2}}
    }
  }
}`

func TestParseVerbosePid(t *testing.T) {
	t.Run("should read containerd's pid", func(t *testing.T) {
		pid, err := ParseVerbosePid(map[string]string{"info": containerdInfo})
		require.NoError(t, err)
		require.Equal(t, uint32(4242), pid)
	})

	t.Run("should read cri-o's pid", func(t *testing.T) {
		pid, err := ParseVerbosePid(map[string]string{"info": crioInfo})
		require.NoError(t, err)
		require.Equal(t, uint32(5353), pid)
	})

	t.Run("should read a sandbox's pid", func(t *testing.T) {
		pid, err := ParseVerbosePid(map[string]string{"info": containerdSandboxInfo})
		require.NoError(t, err)
		require.Equal(t, uint32(7171), pid)
	})

	t.Run("should reject a response with no info key", func(t *testing.T) {
		_, err := ParseVerbosePid(map[string]string{"other": "{}"})
		require.ErrorContains(t, err, "info")
	})

	t.Run("should reject info that is not json", func(t *testing.T) {
		_, err := ParseVerbosePid(map[string]string{"info": "not json"})
		require.Error(t, err)
	})

	t.Run("should reject a pid of zero", func(t *testing.T) {
		// No process has pid 0, so a zero here is the field absent rather than a
		// pid worth reporting -- and a caller that took it for one would compare
		// a runtime's honest answer against a number we invented.
		_, err := ParseVerbosePid(map[string]string{"info": `{"runtimeSpec": {}}`})
		require.ErrorContains(t, err, "pid")
	})
}

// The termination reason and message are the oracle for the two Container
// fields of the same name, and CRI carries them on the status rather than in
// the spec.
func TestInspectorSurfacesTermination(t *testing.T) {
	t.Run("should report the reason and message of an exited container", func(t *testing.T) {
		rt := &fakeRuntime{ctrStatus: &runtimeapi.ContainerStatusResponse{
			Status: &runtimeapi.ContainerStatus{
				State:    runtimeapi.ContainerState_CONTAINER_EXITED,
				ExitCode: 3,
				Reason:   "Error",
				Message:  "command terminated with exit code 3",
			},
			Info: map[string]string{"info": containerdInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).Inspect(context.Background(), "ctr0")
		require.NoError(t, err)
		require.Equal(t, "Error", o.Reason)
		require.Equal(t, "command terminated with exit code 3", o.Message)
	})

	t.Run("should report neither for a running container", func(t *testing.T) {
		rt := &fakeRuntime{ctrStatus: &runtimeapi.ContainerStatusResponse{
			Status: &runtimeapi.ContainerStatus{State: runtimeapi.ContainerState_CONTAINER_RUNNING},
			Info:   map[string]string{"info": containerdInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).Inspect(context.Background(), "ctr0")
		require.NoError(t, err)
		require.Empty(t, o.Reason)
		require.Empty(t, o.Message)
	})
}

func TestInspectorSurfacesPid(t *testing.T) {
	t.Run("should report the pid alongside the spec", func(t *testing.T) {
		rt := &fakeRuntime{ctrStatus: &runtimeapi.ContainerStatusResponse{
			Status: &runtimeapi.ContainerStatus{State: runtimeapi.ContainerState_CONTAINER_RUNNING},
			Info:   map[string]string{"info": containerdInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).Inspect(context.Background(), "ctr0")
		require.NoError(t, err)
		require.Equal(t, uint32(4242), o.Pid)
		require.NotNil(t, o.Spec, "the spec must still be read")
		require.Equal(t, framework.StateRunning, o.State)
		require.Equal(t, []bool{true}, rt.ctrStatusVerbose,
			"the status must be asked for verbosely or it carries no spec")
	})

	t.Run("should tolerate a container with no pid yet", func(t *testing.T) {
		// Inspect is on the path of every adjustment test, including containers
		// inspected before they are started, where the runtime reports no pid.
		// Failing here would turn an addition into a regression of that suite.
		rt := &fakeRuntime{ctrStatus: &runtimeapi.ContainerStatusResponse{
			Status: &runtimeapi.ContainerStatus{State: runtimeapi.ContainerState_CONTAINER_CREATED},
			Info:   map[string]string{"info": `{"runtimeSpec": {"ociVersion": "1.2.0"}}`},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).Inspect(context.Background(), "ctr0")
		require.NoError(t, err)
		require.Zero(t, o.Pid)
		require.NotNil(t, o.Spec)
	})
}

func TestInspectPod(t *testing.T) {
	t.Run("should report the primary ip first and then the additional ones", func(t *testing.T) {
		rt := &fakeRuntime{podStatus: &runtimeapi.PodSandboxStatusResponse{
			Status: &runtimeapi.PodSandboxStatus{
				Network: &runtimeapi.PodSandboxNetworkStatus{
					Ip:            "10.217.0.5",
					AdditionalIps: []*runtimeapi.PodIP{{Ip: "10.217.0.6"}},
				},
			},
			Info: map[string]string{"info": containerdSandboxInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).InspectPod(context.Background(), "pod0")
		require.NoError(t, err)
		require.Equal(t, []string{"10.217.0.5", "10.217.0.6"}, o.IPs)
		require.Equal(t, uint32(7171), o.Pid)
	})

	t.Run("should report no ips for a sandbox with no network", func(t *testing.T) {
		// A host-network sandbox has no network status of its own. That is not an
		// error, and a reader that took it for one would report every such pod as
		// an unreadable one.
		rt := &fakeRuntime{podStatus: &runtimeapi.PodSandboxStatusResponse{
			Status: &runtimeapi.PodSandboxStatus{},
			Info:   map[string]string{"info": containerdSandboxInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).InspectPod(context.Background(), "pod0")
		require.NoError(t, err)
		require.Empty(t, o.IPs)
	})

	// A sandbox's spec is the only oracle for what the runtime realized for the
	// pod itself: its cgroup, its namespaces and its resources.
	t.Run("should report the sandbox's own OCI spec", func(t *testing.T) {
		rt := &fakeRuntime{podStatus: &runtimeapi.PodSandboxStatusResponse{
			Status: &runtimeapi.PodSandboxStatus{},
			Info:   map[string]string{"info": containerdSandboxInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).InspectPod(context.Background(), "pod0")
		require.NoError(t, err)
		require.NotNil(t, o.Spec)
		require.Equal(t, "/kubepods.slice/kubepods-pod123.slice", o.Spec.Linux.CgroupsPath)
		require.Len(t, o.Spec.Linux.Namespaces, 2)
	})

	// CRI-O reports no sandbox pid, so the spec has to be readable without one:
	// the two fail independently.
	t.Run("should report the spec for a sandbox with no pid", func(t *testing.T) {
		rt := &fakeRuntime{podStatus: &runtimeapi.PodSandboxStatusResponse{
			Status: &runtimeapi.PodSandboxStatus{},
			Info:   map[string]string{"info": crioSandboxInfo},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).InspectPod(context.Background(), "pod0")
		require.NoError(t, err)
		require.Zero(t, o.Pid)
		require.NotNil(t, o.Spec)
		require.Equal(t, "pod_123.slice:crio:infra", o.Spec.Linux.CgroupsPath)
	})

	// And a runtime reporting neither is readable too: a sandbox whose spec
	// cannot be had is a field with no oracle, not an unreadable pod.
	t.Run("should report no spec when the runtime offers none", func(t *testing.T) {
		rt := &fakeRuntime{podStatus: &runtimeapi.PodSandboxStatusResponse{
			Status: &runtimeapi.PodSandboxStatus{},
			Info:   map[string]string{"info": `{"pid": 7171}`},
		}}

		o, err := NewInspector(&Client{Runtime: rt}).InspectPod(context.Background(), "pod0")
		require.NoError(t, err)
		require.Equal(t, uint32(7171), o.Pid)
		require.Nil(t, o.Spec)
	})

	t.Run("should name the pod when the status cannot be read", func(t *testing.T) {
		boom := errors.New("sandbox not found")
		rt := &fakeRuntime{podStatusErr: boom}

		_, err := NewInspector(&Client{Runtime: rt}).InspectPod(context.Background(), "pod0")
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "pod0")
	})
}
