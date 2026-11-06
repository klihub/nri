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

// Package k8s runs the suite's workloads through a Kubernetes cluster: the
// conformance driver creates pods through the API server, so kubelet makes
// them the way it does a user's.
package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ownedLabel marks the namespace and the cluster objects as this run's, so
// removal can refuse anything it did not create.
const ownedLabel = "nri-test.containerd.io/owned-by"

// newClientset builds a client from kubeconfig, falling back to client-go's
// usual rules when it is empty.
func newClientset(kubeconfig string) (kubernetes.Interface, error) {
	cs, _, err := newClients(kubeconfig)

	return cs, err
}

// newClients is newClientset that also returns the rest config, which exec
// needs for its streaming connection.
func newClients(kubeconfig string) (kubernetes.Interface, *rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load a kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build a Kubernetes client: %w", err)
	}

	return cs, rc, nil
}
