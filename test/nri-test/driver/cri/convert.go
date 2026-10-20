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
	"fmt"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

// PodSandboxConfig converts a PodSpec into a CRI pod sandbox configuration.
func PodSandboxConfig(cfg *framework.Config, p *framework.PodSpec) *runtimeapi.PodSandboxConfig {
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

	// Every field below lives under Linux, which stays absent when nothing asks
	// for it: a pod spec that sets none of these must reach the runtime exactly
	// as it did before any of them existed.
	linux := func() *runtimeapi.LinuxPodSandboxConfig {
		if c.Linux == nil {
			c.Linux = &runtimeapi.LinuxPodSandboxConfig{}
		}

		return c.Linux
	}

	if p.CgroupParent != "" {
		linux().CgroupParent = p.CgroupParent
	}

	if len(p.Sysctls) > 0 {
		linux().Sysctls = p.Sysctls
	}

	if p.PodOverhead != nil {
		linux().Overhead = linuxResources(p.PodOverhead)
	}

	if p.PodResources != nil {
		linux().Resources = linuxResources(p.PodResources)
	}

	if opts := namespaceOptions(cfg); opts != nil {
		linux().SecurityContext = &runtimeapi.LinuxSandboxSecurityContext{NamespaceOptions: opts}
	}

	return c
}

// namespaceOptions returns the namespace options for the configured mode, or nil
// for cri-api's own defaults. The k8s mode is what kubelet sends for a pod with
// no host namespaces and no shareProcessNamespace: network and IPC shared by the
// pod, and a PID namespace per container.
func namespaceOptions(cfg *framework.Config) *runtimeapi.NamespaceOption {
	if cfg.CRINamespaces != framework.NamespacesK8s {
		return nil
	}

	return &runtimeapi.NamespaceOption{
		Network: runtimeapi.NamespaceMode_POD,
		Pid:     runtimeapi.NamespaceMode_CONTAINER,
		Ipc:     runtimeapi.NamespaceMode_POD,
	}
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
		Image: &runtimeapi.ImageSpec{
			Image: image,
			// Both fields, always, and never one without the other.
			//
			// CRI-O requires UserSpecifiedImage: with it empty it refuses the
			// creation outright, with "user specified image not specified, cannot
			// verify image signature", so a suite that omits it cannot create a
			// single container there. containerd does not require it -- this suite
			// created containers against containerd throughout its existence without
			// ever setting it -- which is why an omission that cost nothing for the
			// suite's whole life turned out to block CRI-O entirely. Image stays as
			// it is: it is what the runtime resolves the image from.
			UserSpecifiedImage: image,
		},
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

	for _, name := range c.CDIDevices {
		config.CDIDevices = append(config.CDIDevices, &runtimeapi.CDIDevice{Name: name})
	}

	// As in PodSandboxConfig: the Linux section and the security context under
	// it are created on demand, so a container spec that sets none of these
	// produces what it produced before they existed. The guard used to be
	// Resources-or-namespaces, and a field added without widening it would be
	// built here and silently dropped.
	linux := func() *runtimeapi.LinuxContainerConfig {
		if config.Linux == nil {
			config.Linux = &runtimeapi.LinuxContainerConfig{}
		}

		return config.Linux
	}

	securityContext := func() *runtimeapi.LinuxContainerSecurityContext {
		if linux().SecurityContext == nil {
			linux().SecurityContext = &runtimeapi.LinuxContainerSecurityContext{}
		}

		return linux().SecurityContext
	}

	if c.Resources != nil {
		linux().Resources = linuxResources(c.Resources)
	}

	if c.OOMScoreAdj != nil {
		if linux().Resources == nil {
			linux().Resources = &runtimeapi.LinuxContainerResources{}
		}
		linux().Resources.OomScoreAdj = *c.OOMScoreAdj
	}

	if c.RunAsUser != nil {
		securityContext().RunAsUser = &runtimeapi.Int64Value{Value: *c.RunAsUser}
	}

	if c.RunAsGroup != nil {
		securityContext().RunAsGroup = &runtimeapi.Int64Value{Value: *c.RunAsGroup}
	}

	if len(c.SupplementalGroups) > 0 {
		securityContext().SupplementalGroups = c.SupplementalGroups
	}

	if c.SeccompProfile != nil {
		securityContext().Seccomp = seccompProfile(c.SeccompProfile)
	}

	if opts := namespaceOptions(cfg); opts != nil {
		securityContext().NamespaceOptions = opts
	}

	return config
}

// seccompProfile converts a SeccompProfile into CRI's security profile.
//
// An unknown type is an error, not a default. CRI's RUNTIME_DEFAULT is the
// zero value, so a typo'd type silently becoming the default is the one
// outcome this must not have: the runtime would then be sent RUNTIME_DEFAULT,
// the fidelity test would still expect UNCONFINED, and the suite would publish
// "this runtime misreports seccomp_profile" about a runtime that reported
// faithfully.
func seccompProfile(p *framework.SeccompProfile) *runtimeapi.SecurityProfile {
	switch p.Type {
	case framework.SeccompUnconfined:
		return &runtimeapi.SecurityProfile{
			ProfileType: runtimeapi.SecurityProfile_Unconfined,
		}
	case framework.SeccompRuntimeDefault:
		return &runtimeapi.SecurityProfile{
			ProfileType: runtimeapi.SecurityProfile_RuntimeDefault,
		}
	}

	// Loud, because the only way here is a test naming a type that does not
	// exist, and the quiet alternative is the defect described above.
	panic(fmt.Sprintf("unknown seccomp profile type %q; use SeccompUnconfined "+
		"or SeccompRuntimeDefault", p.Type))
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
