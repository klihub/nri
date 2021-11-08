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

package api

import (
	fmt "fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/pkg/errors"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	cri "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const (
	// SELinuxRelabel is a Mount pseudo-option to request relabeling.
	SELinuxRelabel = "relabel"

	// ContainerStateUnknown is the initial state for containers.
	ContainerStateUnknown = ContainerState_CONTAINER_UNKNOWN
	// ContainerStateCreated is the state of created containers.
	ContainerStateCreated = ContainerState_CONTAINER_CREATED
	// ContainerStatePaused is the state for paused containers.
	ContainerStatePaused = ContainerState_CONTAINER_PAUSED
	// ContainerStateRunning is the state for running containers.
	ContainerStateRunning = ContainerState_CONTAINER_RUNNING
	// ContainerStateStopped is the state for stopped containers.
	ContainerStateStopped = ContainerState_CONTAINER_STOPPED

	ValidEvents = EventMask((1 << (Event_LAST - 1)) - 1)
)

// nolint
type (
	// Define *Request/*Response type aliases for *Event/Empty pairs.

	StateChangeResponse         = Empty
	RunPodSandboxRequest        = StateChangeEvent
	RunPodSandboxResponse       = Empty
	StopPodSandboxRequest       = StateChangeEvent
	StopPodSandboxResponse      = Empty
	RemovePodSandboxRequest     = StateChangeEvent
	RemovePodSandboxResponse    = Empty
	StartContainerRequest       = StateChangeEvent
	StartContainerResponse      = Empty
	RemoveContainerRequest      = StateChangeEvent
	RemoveContainerResponse     = Empty
	PostCreateContainerRequest  = StateChangeEvent
	PostCreateContainerResponse = Empty
	PostStartContainerRequest   = StateChangeEvent
	PostStartContainerResponse  = Empty
	PostUpdateContainerRequest  = StateChangeEvent
	PostUpdateContainerResponse = Empty

	ShutdownRequest  = Empty
	ShutdownResponse = Empty
)

// EventMask corresponds to a set of enumerated Events.
type EventMask int32

// ToOCI returns an OCI Env entry for the KeyValue.
func (e *KeyValue) ToOCI() string {
	return e.Key + "=" + e.Value
}

// Append appends the given hooks to the existing ones.
func (hooks *Hooks) Append(h *Hooks) *Hooks {
	if h == nil {
		return hooks
	}
	hooks.Prestart = append(hooks.Prestart, h.Prestart...)
	hooks.CreateRuntime = append(hooks.CreateRuntime, h.CreateRuntime...)
	hooks.CreateContainer = append(hooks.CreateContainer, h.CreateContainer...)
	hooks.StartContainer = append(hooks.StartContainer, h.StartContainer...)
	hooks.Poststart = append(hooks.Poststart, h.Poststart...)
	hooks.Poststop = append(hooks.Poststop, h.Poststop...)

	return hooks
}

// Hooks returns itself it any of its hooks is set. Otherwise it returns nil.
func (hooks *Hooks) Hooks() *Hooks {
	if hooks == nil {
		return nil
	}

	if len(hooks.Prestart) > 0 {
		return hooks
	}
	if len(hooks.CreateRuntime) > 0 {
		return hooks
	}
	if len(hooks.CreateContainer) > 0 {
		return hooks
	}
	if len(hooks.StartContainer) > 0 {
		return hooks
	}
	if len(hooks.Poststart) > 0 {
		return hooks
	}
	if len(hooks.Poststop) > 0 {
		return hooks
	}

	return nil
}

// ToOCI returns the hook for an OCI runtime Spec.
func (h *Hook) ToOCI() rspec.Hook {
	return rspec.Hook{
		Path:    h.Path,
		Args:    DupStringSlice(h.Args),
		Env:     DupStringSlice(h.Env),
		Timeout: h.Timeout.Get(),
	}
}

// ToOCI returns resources for an OCI runtime Spec.
func (r *LinuxResources) ToOCI() *rspec.LinuxResources {
	if r == nil {
		return nil
	}
	o := &rspec.LinuxResources{}
	if r.Memory != nil {
		o.Memory = &rspec.LinuxMemory{
			Limit:            r.Memory.Limit.Get(),
			Reservation:      r.Memory.Reservation.Get(),
			Swap:             r.Memory.Swap.Get(),
			Kernel:           r.Memory.Kernel.Get(),
			KernelTCP:        r.Memory.KernelTcp.Get(),
			Swappiness:       r.Memory.Swappiness.Get(),
			DisableOOMKiller: r.Memory.DisableOomKiller.Get(),
			UseHierarchy:     r.Memory.UseHierarchy.Get(),
		}
	}
	if r.Cpu != nil {
		o.CPU = &rspec.LinuxCPU{
			Shares:          r.Cpu.Shares.Get(),
			Quota:           r.Cpu.Quota.Get(),
			Period:          r.Cpu.Period.Get(),
			RealtimeRuntime: r.Cpu.RealtimeRuntime.Get(),
			RealtimePeriod:  r.Cpu.RealtimePeriod.Get(),
			Cpus:            r.Cpu.Cpus,
			Mems:            r.Cpu.Mems,
		}
	}
	for _, l := range r.HugepageLimits {
		o.HugepageLimits = append(o.HugepageLimits, rspec.LinuxHugepageLimit{
			Pagesize: l.PageSize,
			Limit:    l.Limit,
		})
	}
	return o
}

// ToCRI returns resources for CRI.
func (r *LinuxResources) ToCRI(oomScoreAdj int64) *cri.LinuxContainerResources {
	if r == nil {
		return nil
	}
	o := &cri.LinuxContainerResources{}
	if r.Memory != nil {
		o.MemoryLimitInBytes = r.Memory.GetLimit().GetValue()
		o.OomScoreAdj = oomScoreAdj
	}
	if r.Cpu != nil {
		o.CpuShares = int64(r.Cpu.GetShares().GetValue())
		o.CpuPeriod = int64(r.Cpu.GetPeriod().GetValue())
		o.CpuQuota = r.Cpu.GetQuota().GetValue()
		o.CpusetCpus = r.Cpu.Cpus
		o.CpusetMems = r.Cpu.Mems
	}
	for _, l := range r.HugepageLimits {
		o.HugepageLimits = append(o.HugepageLimits, &cri.HugepageLimit{
			PageSize: l.PageSize,
			Limit:    l.Limit,
		})
	}
	return o
}

// Copy creates a copy of the resources.
func (r *LinuxResources) Copy() *LinuxResources {
	if r == nil {
		return nil
	}
	o := &LinuxResources{}
	if r.Memory != nil {
		o.Memory = &LinuxMemory{
			Limit:            Int64(r.Memory.GetLimit()),
			Reservation:      Int64(r.Memory.GetReservation()),
			Swap:             Int64(r.Memory.GetSwap()),
			Kernel:           Int64(r.Memory.GetKernel()),
			KernelTcp:        Int64(r.Memory.GetKernelTcp()),
			Swappiness:       UInt64(r.Memory.GetSwappiness()),
			DisableOomKiller: Bool(r.Memory.GetDisableOomKiller()),
			UseHierarchy:     Bool(r.Memory.GetUseHierarchy()),
		}
	}
	if r.Cpu != nil {
		o.Cpu = &LinuxCPU{
			Shares:          UInt64(r.Cpu.GetShares()),
			Quota:           Int64(r.Cpu.GetQuota()),
			Period:          UInt64(r.Cpu.GetPeriod()),
			RealtimeRuntime: Int64(r.Cpu.GetRealtimeRuntime()),
			RealtimePeriod:  UInt64(r.Cpu.GetRealtimePeriod()),
			Cpus:            r.Cpu.GetCpus(),
			Mems:            r.Cpu.GetMems(),
		}
	}
	for _, l := range r.HugepageLimits {
		o.HugepageLimits = append(o.HugepageLimits, &HugepageLimit{
			PageSize: l.PageSize,
			Limit:    l.Limit,
		})
	}
	return o
}

// ToOCI returns the linux devices for an OCI runtime Spec.
func (d *LinuxDevice) ToOCI() rspec.LinuxDevice {
	if d == nil {
		return rspec.LinuxDevice{}
	}

	return rspec.LinuxDevice{
		Path:     d.Path,
		Type:     d.Type,
		Major:    d.Major,
		Minor:    d.Minor,
		FileMode: d.FileMode.Get(),
		UID:      d.Uid.Get(),
		GID:      d.Gid.Get(),
	}
}

// AccessString returns an OCI access string for the device.
func (d *LinuxDevice) AccessString() string {
	r, w, m := "r", "w", ""

	if mode := d.FileMode.Get(); mode != nil {
		perm := mode.Perm()
		if (perm & 0444) != 0 {
			r = "r"
		}
		if (perm & 0222) != 0 {
			w = "w"
		}
	}
	if d.Type == "b" {
		m = "m"
	}

	return r + w + m
}

// ToOCI returns a Mount for an OCI runtime Spec.
func (m *Mount) ToOCI(propagQuery *string, relabelQuery *bool) rspec.Mount {
	o := rspec.Mount{
		Destination: m.Destination,
		Type:        m.Type,
		Source:      m.Source,
		Options:     []string{},
	}
	propag := ""
	relabel := false
	for _, opt := range m.Options {
		if opt != SELinuxRelabel {
			o.Options = append(o.Options, opt)
			if opt == "rprivate" || opt == "rshared" || opt == "rslave" {
				propag = opt
			}
		} else {
			relabel = true
		}
	}
	if propagQuery != nil {
		*propagQuery = propag
	}
	if relabelQuery != nil {
		*relabelQuery = relabel
	}
	return o
}

// FromOCIEnv returns KeyValues from an OCI runtime Spec environment.
func FromOCIEnv(in []string) []*KeyValue {
	if in == nil {
		return nil
	}
	out := []*KeyValue{}
	for _, keyval := range in {
		var key, val string
		split := strings.SplitN(keyval, "=", 2)
		switch len(split) {
		case 0:
			continue
		case 1:
			key = split[0]
		case 2:
			key = split[0]
			val = split[1]
		default:
			val = strings.Join(split[1:], "=")
		}
		out = append(out, &KeyValue{
			Key:   key,
			Value: val,
		})
	}
	return out
}

// FromOCIHooks returns hooks from an OCI runtime Spec.
func FromOCIHooks(o *rspec.Hooks) *Hooks {
	if o == nil {
		return nil
	}
	return &Hooks{
		Prestart:        FromOCIHookSlice(o.Prestart),
		CreateRuntime:   FromOCIHookSlice(o.CreateRuntime),
		CreateContainer: FromOCIHookSlice(o.CreateContainer),
		StartContainer:  FromOCIHookSlice(o.StartContainer),
		Poststart:       FromOCIHookSlice(o.Poststart),
		Poststop:        FromOCIHookSlice(o.Poststop),
	}
}

// FromOCIHookSlice returns a hook slice from an OCI runtime Spec.
func FromOCIHookSlice(o []rspec.Hook) []*Hook {
	var hooks []*Hook
	for _, h := range o {
		hooks = append(hooks, &Hook{
			Path:    h.Path,
			Args:    DupStringSlice(h.Args),
			Env:     DupStringSlice(h.Env),
			Timeout: Int(h.Timeout),
		})
	}
	return hooks
}

// FromOCIMounts returns a Mount slice for an OCI runtime Spec.
func FromOCIMounts(o []rspec.Mount) []*Mount {
	var mounts []*Mount
	for _, m := range o {
		mounts = append(mounts, &Mount{
			Destination: m.Destination,
			Type:        m.Type,
			Source:      m.Source,
			Options:     DupStringSlice(m.Options),
		})
	}
	return mounts
}

// FromOCILinuxNamespaces returns a namespace slice from an OCI runtime Spec.
func FromOCILinuxNamespaces(o []rspec.LinuxNamespace) []*LinuxNamespace {
	var namespaces []*LinuxNamespace
	for _, ns := range o {
		namespaces = append(namespaces, &LinuxNamespace{
			Type: string(ns.Type),
			Path: ns.Path,
		})
	}
	return namespaces
}

// FromOCILinuxDevices returns a device slice from an OCI runtime Spec.
func FromOCILinuxDevices(o []rspec.LinuxDevice) []*LinuxDevice {
	var devices []*LinuxDevice
	for _, d := range o {
		devices = append(devices, &LinuxDevice{
			Path:     d.Path,
			Type:     d.Type,
			Major:    d.Major,
			Minor:    d.Minor,
			FileMode: FileMode(d.FileMode),
			Uid:      UInt32(d.UID),
			Gid:      UInt32(d.GID),
		})
	}
	return devices
}

// FromOCILinuxResources returns resources from an OCI runtime Spec.
func FromOCILinuxResources(o *rspec.LinuxResources, ann map[string]string) *LinuxResources {
	if o == nil {
		return nil
	}
	l := &LinuxResources{}
	if m := o.Memory; m != nil {
		l.Memory = &LinuxMemory{
			Limit:            Int64(m.Limit),
			Reservation:      Int64(m.Reservation),
			Swap:             Int64(m.Swap),
			Kernel:           Int64(m.Kernel),
			KernelTcp:        Int64(m.KernelTCP),
			Swappiness:       UInt64(m.Swappiness),
			DisableOomKiller: Bool(m.DisableOOMKiller),
			UseHierarchy:     Bool(m.UseHierarchy),
		}
	}
	if c := o.CPU; c != nil {
		l.Cpu = &LinuxCPU{
			Shares:          UInt64(c.Shares),
			Quota:           Int64(c.Quota),
			Period:          UInt64(c.Period),
			RealtimeRuntime: Int64(c.RealtimeRuntime),
			RealtimePeriod:  UInt64(c.RealtimePeriod),
			Cpus:            c.Cpus,
			Mems:            c.Mems,
		}
	}
	for _, h := range o.HugepageLimits {
		l.HugepageLimits = append(l.HugepageLimits, &HugepageLimit{
			PageSize: h.Pagesize,
			Limit:    h.Limit,
		})
	}
	return l
}

// Cmp returns true if the mounts are equal.
func (m *Mount) Cmp(v *Mount) bool {
	if v == nil {
		return false
	}
	if m.Destination != v.Destination || m.Type != v.Type || m.Source != v.Source ||
		len(m.Options) != len(v.Options) {
		return false
	}

	mOpts := make([]string, len(m.Options))
	vOpts := make([]string, len(m.Options))
	sort.Strings(mOpts)
	sort.Strings(vOpts)

	for i, o := range mOpts {
		if vOpts[i] != o {
			return false
		}
	}

	return true
}

// Cmp returns true if the devices are equal.
func (d *LinuxDevice) Cmp(v *LinuxDevice) bool {
	if v == nil {
		return false
	}
	return d.Major != v.Major || d.Minor != v.Minor
}

// DupStringSlice creates a copy of a string slice.
func DupStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// DupStringMap creates a copy of a map with string keys and values.
func DupStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// IsMarkedForRemoval checks if a key is marked for removal.
//
// The key can be an label name, an annotation name, a mount container path,
// a device path, or an environment variable name. These are all marked for
// removal in adjustments by preceding their corresponding key with a '-'.
func IsMarkedForRemoval(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if key[0] != '-' {
		return key, false
	}
	return key[1:], true
}

// MarkForRemoval returns a key marked for removal.
func MarkForRemoval(key string) string {
	return "-" + key
}

// IsMarkedForRemoval checks if an environment variable is marked for removal.
func (e *KeyValue) IsMarkedForRemoval() (string, bool) {
	key, marked := IsMarkedForRemoval(e.Key)
	return key, marked
}

// IsMarkedForRemoval checks if a Mount is marked for removal.
func (m *Mount) IsMarkedForRemoval() (string, bool) {
	key, marked := IsMarkedForRemoval(m.Destination)
	return key, marked
}

// IsMarkedForRemoval checks if a LinuxDevice is marked for removal.
func (d *LinuxDevice) IsMarkedForRemoval() (string, bool) {
	key, marked := IsMarkedForRemoval(d.Path)
	return key, marked
}

// ParsePlugin parses the (file)name of a plugin into an index and a base.
func ParsePlugin(name string) (string, string, error) {
	split := strings.SplitN(name, "-", 2)
	if len(split) < 2 {
		return "", "", errors.Errorf("invalid plugin name %q, idx-pluginname expected", name)
	}
	return split[0], split[1], CheckPluginIndex(split[0])
}

// CheckPluginIndex checks the validity of a plugin index.
func CheckPluginIndex(idx string) error {
	if len(idx) != 2 {
		return errors.Errorf("invalid plugin index %q, must be 2 digits", idx)
	}
	for _, c := range idx {
		if !unicode.IsDigit(c) {
			return errors.Errorf("invalid plugin index %q, must be 2 digits", idx)
		}
	}
	return nil
}

// ParseEventMask parses a string representation into an EventMask.
func ParseEventMask(events ...string) (EventMask, error) {
	var mask EventMask

	bits := map[string]Event{
		"runpodsandbox":       Event_RUN_POD_SANDBOX,
		"stoppodsandbox":      Event_STOP_POD_SANDBOX,
		"removepodsandbox":    Event_REMOVE_POD_SANDBOX,
		"createcontainer":     Event_CREATE_CONTAINER,
		"postcreatecontainer": Event_POST_CREATE_CONTAINER,
		"startcontainer":      Event_START_CONTAINER,
		"poststartcontainer":  Event_POST_START_CONTAINER,
		"updatecontainer":     Event_UPDATE_CONTAINER,
		"postupdatecontainer": Event_POST_UPDATE_CONTAINER,
		"stopcontainer":       Event_STOP_CONTAINER,
		"removecontainer":     Event_REMOVE_CONTAINER,
	}

	for _, event := range events {
		lcEvents := strings.ToLower(event)
		for _, name := range strings.Split(lcEvents, ",") {
			switch name {
			case "all":
				mask |= ValidEvents
				continue
			case "pod", "podsandbox":
				for name, bit := range bits {
					if strings.Contains(name, "Pod") {
						mask.Set(bit)
					}
				}
				continue
			case "container":
				for name, bit := range bits {
					if strings.Contains(name, "Container") {
						mask.Set(bit)
					}
				}
				continue
			}

			bit, ok := bits[strings.TrimSpace(name)]
			if !ok {
				return 0, errors.Errorf("unknown event %q", name)
			}
			mask.Set(bit)
		}
	}

	return mask, nil
}

// MustParseEventMask parses the given events, panic()ing on errors.
func MustParseEventMask(events ...string) EventMask {
	mask, err := ParseEventMask(events...)
	if err != nil {
		panic(fmt.Sprintf("failed to parse events %s", strings.Join(events, " ")))
	}
	return mask
}

// PrettyString returns a human-readable string representation of an EventMask.
func (m *EventMask) PrettyString() string {
	names := map[Event]string{
		Event_RUN_POD_SANDBOX:       "RunPodSandbox",
		Event_STOP_POD_SANDBOX:      "StopPodSandbox",
		Event_REMOVE_POD_SANDBOX:    "RemovePodSandbox",
		Event_CREATE_CONTAINER:      "CreateContainer",
		Event_POST_CREATE_CONTAINER: "PostCreateContainer",
		Event_START_CONTAINER:       "StartContainer",
		Event_POST_START_CONTAINER:  "PostStartContainer",
		Event_UPDATE_CONTAINER:      "UpdateContainer",
		Event_POST_UPDATE_CONTAINER: "PostUpdateContainer",
		Event_STOP_CONTAINER:        "StopContainer",
		Event_REMOVE_CONTAINER:      "RemoveContainer",
	}

	mask := *m
	events, sep := "", ""

	for bit := Event_UNKNOWN + 1; bit <= Event_LAST; bit++ {
		if mask.IsSet(bit) {
			events += sep + names[bit]
			sep = ","
			mask.Clear(bit)
		}
	}

	if mask != 0 {
		events += sep + fmt.Sprintf("unknown(0x%x)", mask)
	}

	return events
}

// Set sets the given Events in the mask.
func (m *EventMask) Set(events ...Event) *EventMask {
	for _, e := range events {
		*m |= (1 << (e - 1))
	}
	return m
}

// Clear clears the given Events in the mask.
func (m *EventMask) Clear(events ...Event) *EventMask {
	for _, e := range events {
		*m &^= (1 << (e - 1))
	}
	return m
}

// IsSet check if the given Event is set in the mask.
func (m *EventMask) IsSet(e Event) bool {
	return *m&(1<<(e-1)) != 0
}
