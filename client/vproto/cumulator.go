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

package vproto

import (
	"github.com/pkg/errors"

	api "github.com/containerd/nri/api/plugin/vproto"
)

type responseCumulator struct {
	request *api.CreateContainerRequest
	hooks   []*api.Hooks
	updates []*api.ContainerUpdate
	updated map[string]*api.ContainerUpdate
	setters map[string]string
}

func newCreateResponseCumulator(request *api.CreateContainerRequest) *responseCumulator {
	if request.Container.LinuxResources == nil {
		request.Container.LinuxResources = &api.LinuxContainerResources{}
	}
	return &responseCumulator{
		request: request,
		hooks:   []*api.Hooks{},
		updates: []*api.ContainerUpdate{},
		updated: map[string]*api.ContainerUpdate{},
		setters: map[string]string{},
	}
}

func newResponseCumulator() *responseCumulator {
	return &responseCumulator{
		updates: []*api.ContainerUpdate{},
		updated: map[string]*api.ContainerUpdate{},
		setters: map[string]string{},
	}
}

func (c *responseCumulator) applyCreateResponse(id string, rpl *api.CreateContainerResponse) error {
	if rpl == nil {
		return nil
	}

	if err := c.addCreate(id, rpl.Create); err != nil {
		return err
	}

	for _, u := range rpl.Updates {
		if err := c.addUpdate(id, u); err != nil {
			return err
		}
	}

	return nil
}

func (c *responseCumulator) applyUpdateResponse(id string, rpl *api.UpdateContainerResponse) error {
	if rpl == nil {
		return nil
	}

	for _, u := range rpl.Updates {
		if err := c.addUpdate(id, u); err != nil {
			return err
		}
	}

	return nil
}

func (c *responseCumulator) applyStopResponse(id string, rpl *api.StopContainerResponse) error {
	if rpl == nil {
		return nil
	}

	for _, u := range rpl.Updates {
		if err := c.addUpdate(id, u); err != nil {
			return err
		}
	}

	return nil
}

func (c *responseCumulator) CreateContainerResponse() *api.CreateContainerResponse {
	return &api.CreateContainerResponse{
		Create: &api.ContainerCreateUpdate{
			LinuxResources: c.request.Container.LinuxResources,
			Labels:         c.request.Container.Labels,
			Annotations:    c.request.Container.Annotations,
			Envs:           c.request.Container.Envs,
			Mounts:         c.request.Container.Mounts,
			Devices:        c.request.Container.Devices,
			Hooks:          c.hooks,
		},
		Updates: c.updates,
	}
}

func (c *responseCumulator) UpdateContainerResponse() *api.UpdateContainerResponse {
	return &api.UpdateContainerResponse{
		Updates: c.updates,
	}
}

func (c *responseCumulator) StopContainerResponse() *api.StopContainerResponse {
	return &api.StopContainerResponse{
		Updates: c.updates,
	}
}

const (
	cpu         = "cpu"
	memory      = "memory"
	hugepage    = "hugepage"
	period      = "period"
	quota       = "quota"
	shares      = "shares"
	limit       = "limit"
	oomScoreAdj = "oomScoreAdj"
	cpuset      = "cpuset"
	env         = "env"
	label       = "label"
	annotation  = "annotation"
	mount       = "mount"
	device      = "device"
)

func ownerKey(id, kind, name string) string {
	return id + ":" + kind + "/" + name
}

func (c *responseCumulator) addCreate(setter string, create *api.ContainerCreateUpdate) error {
	if create == nil {
		return nil
	}

	if err := c.adjustLinuxResources(setter, create.LinuxResources); err != nil {
		return err
	}
	if err := c.adjustLabels(setter, create.Labels); err != nil {
		return err
	}
	if err := c.adjustAnnotations(setter, create.Annotations); err != nil {
		return err
	}
	if err := c.adjustEnvironment(setter, create.Envs); err != nil {
		return err
	}
	if err := c.adjustMounts(setter, create.Mounts); err != nil {
		return err
	}
	if err := c.adjustDevices(setter, create.Devices); err != nil {
		return err
	}

	c.hooks = append(c.hooks, create.Hooks...)

	return nil
}

func (c *responseCumulator) addUpdate(setter string, update *api.ContainerUpdate) error {
	if update == nil {
		return nil
	}

	id := update.ContainerId
	if err := c.updateLinuxResources(id, setter, update.LinuxResources); err != nil {
		return err
	}
	if err := c.updateAnnotations(id, setter, update.Annotations); err != nil {
		return err
	}

	return nil
}

func (c *responseCumulator) adjustLinuxResources(setter string, req *api.LinuxContainerResources) error {
	if req == nil {
		return nil
	}

	if c.request.Container.LinuxResources == nil {
		c.request.Container.LinuxResources = &api.LinuxContainerResources{}
	}

	rpl := c.request.Container.LinuxResources

	if v := req.CpuPeriod; v != 0 && v != rpl.CpuPeriod {
		if err := c.setOwner(ownerKey("", cpu, period), setter); err != nil {
			return err
		}
		rpl.CpuPeriod = v
	}
	if v := req.CpuQuota; v != 0 && v != rpl.CpuQuota {
		if err := c.setOwner(ownerKey("", cpu, quota), setter); err != nil {
			return err
		}
		rpl.CpuQuota = v
	}
	if v := req.CpuShares; v != 0 && v != rpl.CpuShares {
		if err := c.setOwner(ownerKey("", cpu, shares), setter); err != nil {
			return err
		}
		rpl.CpuShares = v
	}

	if v := req.MemoryLimitInBytes; v != 0 && v != rpl.MemoryLimitInBytes {
		if err := c.setOwner(ownerKey("", memory, limit), setter); err != nil {
			return err
		}
		rpl.MemoryLimitInBytes = v
	}
	if v := req.OomScoreAdj; v != 0 && v != rpl.OomScoreAdj {
		if err := c.setOwner(ownerKey("", memory, oomScoreAdj), setter); err != nil {
			return err
		}
		rpl.OomScoreAdj = v
	}

	if v := req.CpusetCpus; v != "" && v != rpl.CpusetCpus {
		if err := c.setOwner(ownerKey("", cpuset, cpu), setter); err != nil {
			return err
		}
		rpl.CpusetCpus = v
	}
	if v := req.CpusetMems; v != "" && v != rpl.CpusetMems {
		if err := c.setOwner(ownerKey("", cpuset, memory), setter); err != nil {
			return err
		}
		rpl.CpusetMems = v
	}

	for _, reql := range req.HugepageLimits {
		rpll := getHugepageLimit(rpl.HugepageLimits, reql.PageSize)
		if rpll == nil || reql.Limit != rpll.Limit {
			if err := c.setOwner(ownerKey("", hugepage, reql.PageSize), setter); err != nil {
				return err
			}
			rpl.HugepageLimits = append(rpl.HugepageLimits, reql)
		}
	}

	return nil
}

func (c *responseCumulator) updateLinuxResources(id, setter string, req *api.LinuxContainerResources) error {
	if req == nil {
		return nil
	}

	u, ok := c.updated[id]
	if !ok {
		u = &api.ContainerUpdate{ContainerId: id}
		c.updates = append(c.updates, u)
		c.updated[id] = u
	}

	if u.LinuxResources == nil {
		u.LinuxResources = &api.LinuxContainerResources{}
	}

	rpl := u.LinuxResources

	if v := req.CpuPeriod; v != 0 && v != rpl.CpuPeriod {
		if err := c.setOwner(ownerKey(id, cpu, period), setter); err != nil {
			return err
		}
		rpl.CpuPeriod = v
	}
	if v := req.CpuQuota; v != 0 && v != rpl.CpuQuota {
		if err := c.setOwner(ownerKey(id, cpu, quota), setter); err != nil {
			return err
		}
		rpl.CpuQuota = v
	}
	if v := req.CpuShares; v != 0 && v != rpl.CpuShares {
		if err := c.setOwner(ownerKey(id, cpu, shares), setter); err != nil {
			return err
		}
		rpl.CpuShares = v
	}

	if v := req.MemoryLimitInBytes; v != 0 && v != rpl.MemoryLimitInBytes {
		if err := c.setOwner(ownerKey(id, memory, limit), setter); err != nil {
			return err
		}
		rpl.MemoryLimitInBytes = v
	}
	if v := req.OomScoreAdj; v != 0 && v != rpl.OomScoreAdj {
		if err := c.setOwner(ownerKey(id, memory, oomScoreAdj), setter); err != nil {
			return err
		}
		rpl.OomScoreAdj = v
	}

	if v := req.CpusetCpus; v != "" && v != rpl.CpusetCpus {
		if err := c.setOwner(ownerKey(id, cpuset, cpu), setter); err != nil {
			return err
		}
		rpl.CpusetCpus = v
	}
	if v := req.CpusetMems; v != "" && v != rpl.CpusetMems {
		if err := c.setOwner(ownerKey(id, cpuset, memory), setter); err != nil {
			return err
		}
		rpl.CpusetMems = v
	}

	for _, reql := range req.HugepageLimits {
		rpll := getHugepageLimit(rpl.HugepageLimits, reql.PageSize)
		if rpll == nil || reql.Limit != rpll.Limit {
			if err := c.setOwner(ownerKey(id, hugepage, reql.PageSize), setter); err != nil {
				return err
			}
			rpl.HugepageLimits = append(rpl.HugepageLimits, reql)
		}
	}

	return nil
}

func (c *responseCumulator) adjustLabels(setter string, req map[string]string) error {
	if len(req) == 0 {
		return nil
	}

	if c.request.Container.Labels == nil {
		c.request.Container.Labels = make(map[string]string)
	}
	for key, value := range req {
		if c.request.Container.Labels[key] != value {
			if err := c.setOwner(ownerKey("", label, key), setter); err != nil {
				return err
			}
			setLabel(c.request.Container.Labels, key, value)
		}
	}

	return nil
}

func (c *responseCumulator) adjustAnnotations(setter string, req map[string]string) error {
	if len(req) == 0 {
		return nil
	}

	if c.request.Container.Annotations == nil {
		c.request.Container.Annotations = make(map[string]string)
	}
	for key, value := range req {
		if c.request.Container.Annotations[key] != value {
			if err := c.setOwner(ownerKey("", annotation, key), setter); err != nil {
				return err
			}
			setAnnotation(c.request.Container.Annotations, key, value)
		}
	}

	return nil
}

func (c *responseCumulator) updateAnnotations(id, setter string, req map[string]string) error {
	if len(req) == 0 {
		return nil
	}

	u, ok := c.updated[id]
	if !ok {
		u = &api.ContainerUpdate{ContainerId: id}
		c.updates = append(c.updates, u)
		c.updated[id] = u
	}

	if u.Annotations == nil {
		u.Annotations = make(map[string]string)
	}

	rpl := u.Annotations

	for key, value := range req {
		if rpl[key] != value {
			if err := c.setOwner(ownerKey(id, annotation, key), setter); err != nil {
				return err
			}
			setAnnotation(rpl, key, value)
		}
	}

	return nil
}

func (c *responseCumulator) adjustEnvironment(setter string, req []*api.KeyValue) error {
	if len(req) == 0 {
		return nil
	}

	for _, e := range req {
		if v := getKeyValue(c.request.Container.Envs, e.Key); v != e.Value {
			if err := c.setOwner(ownerKey("", env, e.Key), setter); err != nil {
				return err
			}
			setEnv(&c.request.Container.Envs, e)
		}
	}

	return nil
}

func (c *responseCumulator) adjustMounts(setter string, req []*api.Mount) error {
	if len(req) == 0 {
		return nil
	}

	for _, m := range req {
		if v := getMount(c.request.Container.Mounts, m.ContainerPath); v == nil || *v != *m {
			if err := c.setOwner(ownerKey("", mount, m.ContainerPath), setter); err != nil {
				return err
			}
			setMount(&c.request.Container.Mounts, m)
		}
	}

	return nil
}

func (c *responseCumulator) adjustDevices(setter string, req []*api.Device) error {
	if len(req) == 0 {
		return nil
	}

	for _, d := range req {
		if v := getDevice(c.request.Container.Devices, d.ContainerPath); v == nil || *v != *d {
			if err := c.setOwner(ownerKey("", device, d.ContainerPath), setter); err != nil {
				return err
			}
			setDevice(&c.request.Container.Devices, d)
		}
	}

	return nil
}

func (c *responseCumulator) setOwner(key, owner string) error {
	if existing, ok := c.setters[key]; ok {
		return errors.Errorf("%s conflict: attempted to set by plugins %s and %s",
			key, existing, owner)
	}
	c.setters[key] = owner
	return nil
}

func getHugepageLimit(limits []*api.HugepageLimit, pageSize string) *api.HugepageLimit {
	for _, l := range limits {
		if l != nil && l.PageSize == pageSize {
			return l
		}
	}
	return nil
}

func getKeyValue(keyValues []*api.KeyValue, key string) string {
	for _, kv := range keyValues {
		if kv != nil && kv.Key != key {
			return kv.Value
		}
	}
	return ""
}

func getMount(mounts []*api.Mount, containerPath string) *api.Mount {
	for _, m := range mounts {
		if m != nil && m.ContainerPath == containerPath {
			return m
		}
	}
	return nil
}

func getDevice(devices []*api.Device, containerPath string) *api.Device {
	for _, d := range devices {
		if d != nil && d.ContainerPath == containerPath {
			return d
		}
	}
	return nil
}

func setLabel(labels map[string]string, key, value string) {
	if value == "" {
		delete(labels, key)
	} else {
		labels[key] = value
	}
}

func setAnnotation(annotations map[string]string, key, value string) {
	if value == "" {
		delete(annotations, key)
	} else {
		annotations[key] = value
	}
}

func setEnv(envsp *[]*api.KeyValue, env *api.KeyValue) {
	envs := []*api.KeyValue{}
	for _, e := range *envsp {
		switch {
		case env == nil || e.Key != env.Key:
			envs = append(envs, e)
		case env.Value != "":
			envs = append(envs, env)
			env = nil
		default:
			env = nil
		}
	}
	if env != nil && env.Key != "" {
		envs = append(envs, env)
	}
	*envsp = envs
}

func setMount(mountsp *[]*api.Mount, mount *api.Mount) {
	mounts := []*api.Mount{}
	for _, m := range *mountsp {
		switch {
		case mount == nil || m.ContainerPath != mount.ContainerPath:
			mounts = append(mounts, m)
		case mount.HostPath != "":
			mounts = append(mounts, mount)
			mount = nil
		default:
			mount = nil
		}
	}
	if mount != nil && mount.HostPath != "" {
		mounts = append(mounts, mount)
	}
	*mountsp = mounts
}

func setDevice(devicesp *[]*api.Device, device *api.Device) {
	devices := []*api.Device{}
	for _, d := range *devicesp {
		switch {
		case device == nil || d.ContainerPath != device.ContainerPath:
			devices = append(devices, d)
		case device.HostPath != "":
			devices = append(devices, device)
			device = nil
		default:
			device = nil
		}
	}
	if device != nil && device.HostPath != "" {
		devices = append(devices, device)
	}
	*devicesp = devices
}
