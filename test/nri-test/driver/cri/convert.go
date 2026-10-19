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
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

// PodSandboxConfig converts a PodSpec into a CRI pod sandbox configuration.
func PodSandboxConfig(p *framework.PodSpec) *runtimeapi.PodSandboxConfig {
	c := &runtimeapi.PodSandboxConfig{
		Metadata: &runtimeapi.PodSandboxMetadata{
			Name:      p.Name,
			Namespace: p.Namespace,
			Uid:       p.UID,
			Attempt:   0,
		},
		Labels:      p.Labels,
		Annotations: p.Annotations,
	}

	if p.CgroupParent != "" {
		c.Linux = &runtimeapi.LinuxPodSandboxConfig{
			CgroupParent: p.CgroupParent,
		}
	}

	return c
}

// ContainerConfig converts a ContainerSpec into a CRI container configuration.
func ContainerConfig(cfg *framework.Config, c *framework.ContainerSpec) *runtimeapi.ContainerConfig {
	image := c.Image
	if image == "" {
		image = cfg.Image
	}

	config := &runtimeapi.ContainerConfig{
		Metadata: &runtimeapi.ContainerMetadata{
			Name:    c.Name,
			Attempt: 0,
		},
		Image:       &runtimeapi.ImageSpec{Image: image},
		Command:     c.Command,
		Args:        c.Args,
		Labels:      c.Labels,
		Annotations: c.Annotations,
	}

	for k, v := range c.Env {
		config.Envs = append(config.Envs, &runtimeapi.KeyValue{Key: k, Value: []byte(v)})
	}

	for _, m := range c.Mounts {
		config.Mounts = append(config.Mounts, &runtimeapi.Mount{
			HostPath:      m.Source,
			ContainerPath: m.Destination,
			Readonly:      m.Readonly,
		})
	}

	if c.Resources != nil {
		config.Linux = &runtimeapi.LinuxContainerConfig{
			Resources: linuxResources(c.Resources),
		}
	}

	return config
}

// ContainerResources converts Resources into a CRI container resource update.
func ContainerResources(r *framework.Resources) *runtimeapi.ContainerResources {
	if r == nil {
		return nil
	}

	return &runtimeapi.ContainerResources{
		Linux: linuxResources(r),
	}
}

// linuxResources converts Resources into CRI Linux container resources. Returns
// nil for a nil r, so that callers can pass a caller-supplied pointer straight
// through without dereferencing it.
func linuxResources(r *framework.Resources) *runtimeapi.LinuxContainerResources {
	if r == nil {
		return nil
	}

	return &runtimeapi.LinuxContainerResources{
		CpuShares:          r.CPUShares,
		CpuQuota:           r.CPUQuota,
		CpuPeriod:          r.CPUPeriod,
		CpusetCpus:         r.CpusetCpus,
		CpusetMems:         r.CpusetMems,
		MemoryLimitInBytes: r.MemoryLimit,
		OomScoreAdj:        r.OOMScoreAdj,
		Unified:            r.Unified,
	}
}
