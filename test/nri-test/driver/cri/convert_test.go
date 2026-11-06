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
	"testing"

	"github.com/stretchr/testify/require"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

func testConfig() *framework.Config {
	return &framework.Config{Image: "registry.k8s.io/pause:3.10", RunID: "run0"}
}

func TestPodSandboxConfig(t *testing.T) {
	t.Run("should carry the pod's identity", func(t *testing.T) {
		cfg := PodSandboxConfig(testConfig(), &framework.PodSpec{
			Name:      "pod0",
			Namespace: "nri-test",
			UID:       "uid0",
		})

		require.Equal(t, "pod0", cfg.Metadata.Name)
		require.Equal(t, "nri-test", cfg.Metadata.Namespace)
		require.Equal(t, "uid0", cfg.Metadata.Uid)
	})

	t.Run("should carry labels and annotations", func(t *testing.T) {
		cfg := PodSandboxConfig(testConfig(), &framework.PodSpec{
			Name:        "pod0",
			Labels:      map[string]string{"a": "1"},
			Annotations: map[string]string{"b": "2"},
		})

		require.Equal(t, "1", cfg.Labels["a"])
		require.Equal(t, "2", cfg.Annotations["b"])
	})

	t.Run("should set the cgroup parent when given", func(t *testing.T) {
		cfg := PodSandboxConfig(testConfig(), &framework.PodSpec{
			Name:         "pod0",
			CgroupParent: "/kubepods",
		})

		require.Equal(t, "/kubepods", cfg.Linux.CgroupParent)
	})
}

func TestContainerConfig(t *testing.T) {
	t.Run("should default the image to the configured one", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{Name: "ctr0"})
		require.Equal(t, "registry.k8s.io/pause:3.10", cfg.Image.Image)
	})

	t.Run("should keep an explicit image", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{
			Name:  "ctr0",
			Image: "docker.io/library/busybox:latest",
		})
		require.Equal(t, "docker.io/library/busybox:latest", cfg.Image.Image)
	})

	t.Run("should carry the command, args and environment", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{
			Name:    "ctr0",
			Command: []string{"/bin/sh"},
			Args:    []string{"-c", "sleep 1"},
			Env:     map[string]string{"K": "V"},
		})

		require.Equal(t, []string{"/bin/sh"}, cfg.Command)
		require.Equal(t, []string{"-c", "sleep 1"}, cfg.Args)
		require.Len(t, cfg.Envs, 1)
		require.Equal(t, "K", cfg.Envs[0].Key)
		require.Equal(t, []byte("V"), cfg.Envs[0].Value)
	})

	t.Run("should carry mounts", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{
			Name: "ctr0",
			Mounts: []*framework.Mount{
				{Source: "/tmp", Destination: "/host-tmp", Readonly: true},
			},
		})

		require.Len(t, cfg.Mounts, 1)
		require.Equal(t, "/tmp", cfg.Mounts[0].HostPath)
		require.Equal(t, "/host-tmp", cfg.Mounts[0].ContainerPath)
		require.True(t, cfg.Mounts[0].Readonly)
	})

	t.Run("should carry initial resources", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{
			Name:      "ctr0",
			Resources: &framework.Resources{CPUShares: 512, MemoryLimit: 1 << 20},
		})

		require.Equal(t, int64(512), cfg.Linux.Resources.CpuShares)
		require.Equal(t, int64(1<<20), cfg.Linux.Resources.MemoryLimitInBytes)
	})

	t.Run("should omit the linux section without resources", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{Name: "ctr0"})
		require.Nil(t, cfg.Linux)
	})
}

func TestContainerConfigSetsUserSpecifiedImage(t *testing.T) {
	// CRI-O requires ContainerConfig.Image.UserSpecifiedImage: with it empty it
	// refuses the creation outright, with "user specified image not specified,
	// cannot verify image signature", so a suite that leaves it unset cannot
	// create a single container there. containerd does not require it -- this
	// suite created containers against containerd throughout its existence
	// without ever setting it -- which is why the omission cost nothing for the
	// suite's whole life and then cost everything.
	//
	// Both fields are asserted, every time. A change that set UserSpecifiedImage
	// and dropped Image would satisfy a test that only looked at the new field,
	// and would break every runtime there is.
	t.Run("should set both image fields from an explicit image", func(t *testing.T) {
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{
			Name:  "ctr0",
			Image: "docker.io/library/busybox:latest",
		})

		require.Equal(t, "docker.io/library/busybox:latest", cfg.Image.Image)
		require.Equal(t, "docker.io/library/busybox:latest", cfg.Image.UserSpecifiedImage)
	})

	t.Run("should set both image fields from the configured image", func(t *testing.T) {
		// The fallback to Config.Image has to reach both fields too: a spec with no
		// image of its own is the ordinary case -- CtrSpec fills it in, but PodSpec's
		// own containers need not -- so a fallback that filled only Image would
		// leave CRI-O refusing exactly those containers.
		cfg := ContainerConfig(testConfig(), &framework.ContainerSpec{Name: "ctr0"})

		require.Equal(t, "registry.k8s.io/pause:3.10", cfg.Image.Image)
		require.Equal(t, "registry.k8s.io/pause:3.10", cfg.Image.UserSpecifiedImage)
	})
}

func TestLinuxResources(t *testing.T) {
	t.Run("should be nil for nil resources", func(t *testing.T) {
		// It used to dereference its argument, which UpdatePodResources could
		// reach with a nil r.
		require.Nil(t, linuxResources(nil))
	})

	t.Run("should carry every field", func(t *testing.T) {
		r := linuxResources(&framework.Resources{
			CPUShares:   512,
			CPUQuota:    20000,
			CPUPeriod:   100000,
			CpusetCpus:  "0-1",
			CpusetMems:  "0",
			MemoryLimit: 1 << 21,
			OOMScoreAdj: 42,
			Unified:     map[string]string{"memory.high": "1048576"},
		})

		require.Equal(t, int64(512), r.CpuShares)
		require.Equal(t, int64(20000), r.CpuQuota)
		require.Equal(t, int64(100000), r.CpuPeriod)
		require.Equal(t, "0-1", r.CpusetCpus)
		require.Equal(t, "0", r.CpusetMems)
		require.Equal(t, int64(1<<21), r.MemoryLimitInBytes)
		require.Equal(t, int64(42), r.OomScoreAdj)
		require.Equal(t, "1048576", r.Unified["memory.high"])
	})
}

func TestContainerResources(t *testing.T) {
	t.Run("should be nil for nil resources", func(t *testing.T) {
		require.Nil(t, ContainerResources(nil))
	})

	t.Run("should carry cpu and memory settings", func(t *testing.T) {
		r := ContainerResources(&framework.Resources{
			CPUShares:   512,
			CPUQuota:    20000,
			CPUPeriod:   100000,
			CpusetCpus:  "0-1",
			CpusetMems:  "0",
			MemoryLimit: 1 << 21,
			Unified:     map[string]string{"memory.high": "1048576"},
		})

		require.Equal(t, int64(512), r.Linux.CpuShares)
		require.Equal(t, int64(20000), r.Linux.CpuQuota)
		require.Equal(t, int64(100000), r.Linux.CpuPeriod)
		require.Equal(t, "0-1", r.Linux.CpusetCpus)
		require.Equal(t, "0", r.Linux.CpusetMems)
		require.Equal(t, int64(1<<21), r.Linux.MemoryLimitInBytes)
		require.Equal(t, "1048576", r.Linux.Unified["memory.high"])
	})
}

func TestNamespaceOptions(t *testing.T) {
	k8s := testConfig()
	k8s.CRINamespaces = framework.NamespacesK8s

	t.Run("should send kubelet's options in k8s mode", func(t *testing.T) {
		pod := PodSandboxConfig(k8s, &framework.PodSpec{Name: "pod0"})
		ctr := ContainerConfig(k8s, &framework.ContainerSpec{Name: "ctr0"})

		for _, o := range []*runtimeapi.NamespaceOption{
			pod.Linux.SecurityContext.NamespaceOptions,
			ctr.Linux.SecurityContext.NamespaceOptions,
		} {
			require.Equal(t, runtimeapi.NamespaceMode_CONTAINER, o.Pid)
			require.Equal(t, runtimeapi.NamespaceMode_POD, o.Network)
			require.Equal(t, runtimeapi.NamespaceMode_POD, o.Ipc)
		}
	})

	t.Run("should keep the cgroup parent and resources in k8s mode", func(t *testing.T) {
		pod := PodSandboxConfig(k8s, &framework.PodSpec{Name: "pod0", CgroupParent: "/kubepods"})
		ctr := ContainerConfig(k8s, &framework.ContainerSpec{
			Name:      "ctr0",
			Resources: &framework.Resources{CPUShares: 512},
		})

		require.Equal(t, "/kubepods", pod.Linux.CgroupParent)
		require.Equal(t, int64(512), ctr.Linux.Resources.CpuShares)
	})

	t.Run("should send none in cri mode", func(t *testing.T) {
		cri := testConfig()
		cri.CRINamespaces = framework.NamespacesCRI

		require.Nil(t, PodSandboxConfig(cri, &framework.PodSpec{Name: "pod0"}).Linux)
		require.Nil(t, ContainerConfig(cri, &framework.ContainerSpec{Name: "ctr0"}).Linux)
	})
}
