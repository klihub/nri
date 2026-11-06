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

package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/containerd/nri/test/nri-test/driver/cri"
	"github.com/containerd/nri/test/nri-test/framework"
)

// Inspector observes containers of the pods this driver created.
//
// Inspect stays with the runtime, because the realized OCI spec is only
// there. Exec goes through the API server, as kubectl exec does, so it takes
// the kubelet's streaming path instead of the runtime's ExecSync.
type Inspector struct {
	*cri.Inspector

	cs kubernetes.Interface
	rc *rest.Config
	ns string
	// runID scopes the pod lookup behind Exec to this run's own workloads.
	// See find.
	runID string
}

var (
	_ framework.Inspector = &Inspector{}
	_ framework.Executor  = &Inspector{}
)

// Exec runs cmd in the container with the given runtime id.
func (i *Inspector) Exec(ctx context.Context, containerID string, cmd []string) (string, string, error) {
	pod, name, err := i.find(ctx, containerID)
	if err != nil {
		return "", "", err
	}

	req := i.cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: name,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	ex, err := remotecommand.NewSPDYExecutor(i.rc, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("failed to set up exec in container %s: %w", containerID, err)
	}

	var stdout, stderr bytes.Buffer

	err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf(
			"failed to exec %v in container %s: %w", cmd, containerID, err)
	}

	return stdout.String(), stderr.String(), nil
}

// find locates the pod and container name for a runtime container id.
//
// Scoped to this run's own id, not merely to pods that carry the label at all.
// What this returns is handed to Exec, which runs a command inside the
// container, so it is a destructive-shaped predicate rather than an
// observation: selecting on the label's presence put a concurrent run's pods
// within reach of it, which is exactly what Scope.MatchesForRemoval refuses to
// do for removal and for the same reason -- a run id cannot tell a crashed
// peer's leftovers from a live peer's workloads, so neither may be touched.
//
// With no run id of our own, nothing can be identified as ours and the lookup
// matches nothing. That fails closed, as the removal predicate does.
func (i *Inspector) find(ctx context.Context, id string) (*corev1.Pod, string, error) {
	if i.runID == "" {
		return nil, "", fmt.Errorf("this run has no run id, so container %s "+
			"cannot be identified as one of its own", id)
	}

	pods, err := i.cs.CoreV1().Pods(i.ns).List(ctx, metav1.ListOptions{
		LabelSelector: framework.RunIDLabel + "=" + i.runID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to list pods: %w", err)
	}

	for p := range pods.Items {
		for _, cs := range pods.Items[p].Status.ContainerStatuses {
			if trimScheme(cs.ContainerID) == id {
				return &pods.Items[p], cs.Name, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no pod of this run has container %s", id)
}

// kubeletPolicies are the kubelet settings that decide what a container's
// cpuset and memory nodes are before an NRI plugin touches them.
type kubeletPolicies struct {
	CPUManager    string `json:"cpuManagerPolicy"`
	MemoryManager string `json:"memoryManagerPolicy"`
}

// parseKubeletPolicies reads the kubelet's /configz response.
func parseKubeletPolicies(raw []byte) (kubeletPolicies, error) {
	var out struct {
		Config kubeletPolicies `json:"kubeletconfig"`
	}

	if err := json.Unmarshal(raw, &out); err != nil {
		return kubeletPolicies{}, fmt.Errorf("failed to parse the kubelet configuration: %w", err)
	}

	return out.Config, nil
}

// check refuses a pinning policy. With the CPU and memory managers off, a
// container starts with the node's full cpuset and memory nodes, which is what
// the suite expects to compare an NRI adjustment against. An unset policy is
// "none", the kubelet default.
func (p kubeletPolicies) check(node string) error {
	var errs []error

	for _, f := range []struct{ name, value string }{
		{"cpuManagerPolicy", p.CPUManager},
		{"memoryManagerPolicy", p.MemoryManager},
	} {
		if v := strings.ToLower(f.value); v != "" && v != "none" {
			errs = append(errs, fmt.Errorf("the kubelet on node %s has %s %q; "+
				"set it to none so containers start unpinned", node, f.name, f.value))
		}
	}

	return errors.Join(errs...)
}

// checkKubeletPolicies reads the node's kubelet configuration through the API
// server and refuses a pinning policy; see kubeletPolicies.check.
func checkKubeletPolicies(ctx context.Context, cs kubernetes.Interface, node string) error {
	raw, err := cs.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes/" + node + "/proxy/configz").DoRaw(ctx)
	if err != nil {
		return fmt.Errorf("failed to read the kubelet configuration of node %s: %w", node, err)
	}

	p, err := parseKubeletPolicies(raw)
	if err != nil {
		return err
	}

	return p.check(node)
}
