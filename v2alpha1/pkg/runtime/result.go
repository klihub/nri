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

package runtime

import (
	"github.com/pkg/errors"
)

type resultCollector struct {
	request *CreateContainerRequest
	hooks   *Hooks
	updates []*ContainerAdjustment
	updated map[string]*ContainerAdjustment
	setters map[string]string
}

func newCreateResultCollector(request *CreateContainerRequest) *resultCollector {
	if request.Container.LinuxResources == nil {
		request.Container.LinuxResources = &LinuxContainerResources{}
	}
	return &resultCollector{
		request: request,
		hooks:   &Hooks{},
		updates: []*ContainerAdjustment{},
		updated: map[string]*ContainerAdjustment{},
		setters: map[string]string{},
	}
}

func newResultCollector() *resultCollector {
	return &resultCollector{
		updates: []*ContainerAdjustment{},
		updated: map[string]*ContainerAdjustment{},
		setters: map[string]string{},
	}
}

func (r *resultCollector) applyCreateResponse(id string, rpl *CreateContainerResponse) error {
	if rpl == nil {
		return nil
	}

	if err := r.addCreate(id, rpl.Create); err != nil {
		return err
	}

	for _, u := range rpl.Adjust {
		if err := r.addUpdate(id, u); err != nil {
			return err
		}
	}

	return nil
}

func (r *resultCollector) applyUpdateResponse(id string, rpl *UpdateContainerResponse) error {
	if rpl == nil {
		return nil
	}

	for _, u := range rpl.Adjust {
		if err := r.addUpdate(id, u); err != nil {
			return err
		}
	}

	return nil
}

func (r *resultCollector) applyStopResponse(id string, rpl *StopContainerResponse) error {
	if rpl == nil {
		return nil
	}

	for _, u := range rpl.Adjust {
		if err := r.addUpdate(id, u); err != nil {
			return err
		}
	}

	return nil
}

func (r *resultCollector) CreateContainerResponse() *CreateContainerResponse {
	return &CreateContainerResponse{
		Create: &ContainerCreateAdjustment{
			LinuxResources: r.request.Container.LinuxResources,
			Labels:         r.request.Container.Labels,
			Annotations:    r.request.Container.Annotations,
			Envs:           r.request.Container.Envs,
			Mounts:         r.request.Container.Mounts,
			Devices:        r.request.Container.Devices,
			Hooks:          r.hooks,
		},
		Adjust: r.updates,
	}
}

func (r *resultCollector) UpdateContainerResponse() *UpdateContainerResponse {
	return &UpdateContainerResponse{
		Adjust: r.updates,
	}
}

func (r *resultCollector) StopContainerResponse() *StopContainerResponse {
	return &StopContainerResponse{
		Adjust: r.updates,
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

func (r *resultCollector) addCreate(setter string, create *ContainerCreateAdjustment) error {
	if create == nil {
		return nil
	}

	if err := r.adjustLinuxResources(setter, create.LinuxResources); err != nil {
		return err
	}
	if err := r.adjustLabels(setter, create.Labels); err != nil {
		return err
	}
	if err := r.adjustAnnotations(setter, create.Annotations); err != nil {
		return err
	}
	if err := r.adjustEnvironment(setter, create.Envs); err != nil {
		return err
	}
	if err := r.adjustMounts(setter, create.Mounts); err != nil {
		return err
	}
	if err := r.adjustDevices(setter, create.Devices); err != nil {
		return err
	}

	r.hooks.Append(create.Hooks)

	return nil
}

func (r *resultCollector) addUpdate(setter string, update *ContainerAdjustment) error {
	if update == nil {
		return nil
	}

	id := update.ContainerId
	if err := r.updateLinuxResources(id, setter, update.LinuxResources); err != nil {
		return err
	}
	if err := r.updateAnnotations(id, setter, update.Annotations); err != nil {
		return err
	}

	return nil
}

func (r *resultCollector) adjustLinuxResources(setter string, req *LinuxContainerResources) error {
	if req == nil {
		return nil
	}

	if r.request.Container.LinuxResources == nil {
		r.request.Container.LinuxResources = &LinuxContainerResources{}
	}

	rpl := r.request.Container.LinuxResources

	if v := req.CpuPeriod; v != 0 && v != rpl.CpuPeriod {
		if err := r.setOwner(ownerKey("", cpu, period), setter); err != nil {
			return err
		}
		rpl.CpuPeriod = v
	}
	if v := req.CpuQuota; v != 0 && v != rpl.CpuQuota {
		if err := r.setOwner(ownerKey("", cpu, quota), setter); err != nil {
			return err
		}
		rpl.CpuQuota = v
	}
	if v := req.CpuShares; v != 0 && v != rpl.CpuShares {
		if err := r.setOwner(ownerKey("", cpu, shares), setter); err != nil {
			return err
		}
		rpl.CpuShares = v
	}

	if v := req.MemoryLimitInBytes; v != 0 && v != rpl.MemoryLimitInBytes {
		if err := r.setOwner(ownerKey("", memory, limit), setter); err != nil {
			return err
		}
		rpl.MemoryLimitInBytes = v
	}
	if v := req.OomScoreAdj; v != 0 && v != rpl.OomScoreAdj {
		if err := r.setOwner(ownerKey("", memory, oomScoreAdj), setter); err != nil {
			return err
		}
		rpl.OomScoreAdj = v
	}

	if v := req.CpusetCpus; v != "" && v != rpl.CpusetCpus {
		if err := r.setOwner(ownerKey("", cpuset, cpu), setter); err != nil {
			return err
		}
		rpl.CpusetCpus = v
	}
	if v := req.CpusetMems; v != "" && v != rpl.CpusetMems {
		if err := r.setOwner(ownerKey("", cpuset, memory), setter); err != nil {
			return err
		}
		rpl.CpusetMems = v
	}

	for _, reql := range req.HugepageLimits {
		rpll := getHugepageLimit(rpl.HugepageLimits, reql.PageSize)
		if rpll == nil || reql.Limit != rpll.Limit {
			if err := r.setOwner(ownerKey("", hugepage, reql.PageSize), setter); err != nil {
				return err
			}
			rpl.HugepageLimits = append(rpl.HugepageLimits, reql)
		}
	}

	return nil
}

func (r *resultCollector) updateLinuxResources(id, setter string, req *LinuxContainerResources) error {
	if req == nil {
		return nil
	}

	u, ok := r.updated[id]
	if !ok {
		u = &ContainerAdjustment{ContainerId: id}
		r.updates = append(r.updates, u)
		r.updated[id] = u
	}

	if u.LinuxResources == nil {
		u.LinuxResources = &LinuxContainerResources{}
	}

	rpl := u.LinuxResources

	if v := req.CpuPeriod; v != 0 && v != rpl.CpuPeriod {
		if err := r.setOwner(ownerKey(id, cpu, period), setter); err != nil {
			return err
		}
		rpl.CpuPeriod = v
	}
	if v := req.CpuQuota; v != 0 && v != rpl.CpuQuota {
		if err := r.setOwner(ownerKey(id, cpu, quota), setter); err != nil {
			return err
		}
		rpl.CpuQuota = v
	}
	if v := req.CpuShares; v != 0 && v != rpl.CpuShares {
		if err := r.setOwner(ownerKey(id, cpu, shares), setter); err != nil {
			return err
		}
		rpl.CpuShares = v
	}

	if v := req.MemoryLimitInBytes; v != 0 && v != rpl.MemoryLimitInBytes {
		if err := r.setOwner(ownerKey(id, memory, limit), setter); err != nil {
			return err
		}
		rpl.MemoryLimitInBytes = v
	}
	if v := req.OomScoreAdj; v != 0 && v != rpl.OomScoreAdj {
		if err := r.setOwner(ownerKey(id, memory, oomScoreAdj), setter); err != nil {
			return err
		}
		rpl.OomScoreAdj = v
	}

	if v := req.CpusetCpus; v != "" && v != rpl.CpusetCpus {
		if err := r.setOwner(ownerKey(id, cpuset, cpu), setter); err != nil {
			return err
		}
		rpl.CpusetCpus = v
	}
	if v := req.CpusetMems; v != "" && v != rpl.CpusetMems {
		if err := r.setOwner(ownerKey(id, cpuset, memory), setter); err != nil {
			return err
		}
		rpl.CpusetMems = v
	}

	for _, reql := range req.HugepageLimits {
		rpll := getHugepageLimit(rpl.HugepageLimits, reql.PageSize)
		if rpll == nil || reql.Limit != rpll.Limit {
			if err := r.setOwner(ownerKey(id, hugepage, reql.PageSize), setter); err != nil {
				return err
			}
			rpl.HugepageLimits = append(rpl.HugepageLimits, reql)
		}
	}

	return nil
}

func (r *resultCollector) adjustLabels(setter string, req map[string]string) error {
	if len(req) == 0 {
		return nil
	}

	if r.request.Container.Labels == nil {
		r.request.Container.Labels = make(map[string]string)
	}
	for key, value := range req {
		if r.request.Container.Labels[key] != value {
			if err := r.setOwner(ownerKey("", label, key), setter); err != nil {
				return err
			}
			setLabel(r.request.Container.Labels, key, value)
		}
	}

	return nil
}

func (r *resultCollector) adjustAnnotations(setter string, req map[string]string) error {
	if len(req) == 0 {
		return nil
	}

	if r.request.Container.Annotations == nil {
		r.request.Container.Annotations = make(map[string]string)
	}
	for key, value := range req {
		if r.request.Container.Annotations[key] != value {
			if err := r.setOwner(ownerKey("", annotation, key), setter); err != nil {
				return err
			}
			setAnnotation(r.request.Container.Annotations, key, value)
		}
	}

	return nil
}

func (r *resultCollector) updateAnnotations(id, setter string, req map[string]string) error {
	if len(req) == 0 {
		return nil
	}

	u, ok := r.updated[id]
	if !ok {
		u = &ContainerAdjustment{ContainerId: id}
		r.updates = append(r.updates, u)
		r.updated[id] = u
	}

	if u.Annotations == nil {
		u.Annotations = make(map[string]string)
	}

	rpl := u.Annotations

	for key, value := range req {
		if rpl[key] != value {
			if err := r.setOwner(ownerKey(id, annotation, key), setter); err != nil {
				return err
			}
			setAnnotation(rpl, key, value)
		}
	}

	return nil
}

func (r *resultCollector) adjustEnvironment(setter string, req []*KeyValue) error {
	if len(req) == 0 {
		return nil
	}

	for _, e := range req {
		if v := getKeyValue(r.request.Container.Envs, e.Key); v != e.Value {
			if err := r.setOwner(ownerKey("", env, e.Key), setter); err != nil {
				return err
			}
			setEnv(&r.request.Container.Envs, e)
		}
	}

	return nil
}

func (r *resultCollector) adjustMounts(setter string, req []*Mount) error {
	if len(req) == 0 {
		return nil
	}

	for _, m := range req {
		if v := getMount(r.request.Container.Mounts, m.ContainerPath); !m.Cmp(v) {
			if err := r.setOwner(ownerKey("", mount, m.ContainerPath), setter); err != nil {
				return err
			}
			setMount(&r.request.Container.Mounts, m)
		}
	}

	return nil
}

func (r *resultCollector) adjustDevices(setter string, req []*Device) error {
	if len(req) == 0 {
		return nil
	}

	for _, d := range req {
		if v := getDevice(r.request.Container.Devices, d.ContainerPath); !d.Cmp(v) {
			if err := r.setOwner(ownerKey("", device, d.ContainerPath), setter); err != nil {
				return err
			}
			setDevice(&r.request.Container.Devices, d)
		}
	}

	return nil
}

func (r *resultCollector) setOwner(key, owner string) error {
	if existing, ok := r.setters[key]; ok {
		return errors.Errorf("%s conflict: attempted to set by plugins %s and %s",
			key, existing, owner)
	}
	r.setters[key] = owner
	return nil
}

func getHugepageLimit(limits []*HugepageLimit, pageSize string) *HugepageLimit {
	for _, l := range limits {
		if l != nil && l.PageSize == pageSize {
			return l
		}
	}
	return nil
}

func getKeyValue(keyValues []*KeyValue, key string) string {
	for _, kv := range keyValues {
		if kv != nil && kv.Key != key {
			return kv.Value
		}
	}
	return ""
}

func getMount(mounts []*Mount, containerPath string) *Mount {
	for _, m := range mounts {
		if m != nil && m.ContainerPath == containerPath {
			return m
		}
	}
	return nil
}

func getDevice(devices []*Device, containerPath string) *Device {
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

func setEnv(envsp *[]*KeyValue, env *KeyValue) {
	envs := []*KeyValue{}
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

func setMount(mountsp *[]*Mount, mount *Mount) {
	mounts := []*Mount{}
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

func setDevice(devicesp *[]*Device, device *Device) {
	devices := []*Device{}
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


