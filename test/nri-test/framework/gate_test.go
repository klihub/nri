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

package framework

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// gateDriver is a Driver that only answers the questions a gate asks. Every
// workload method is unreachable from a gate, so they are left unimplemented.
type gateDriver struct {
	name string
	caps Capability
}

func (d *gateDriver) Name() string                { return d.name }
func (d *gateDriver) Capabilities() Capability    { return d.caps }
func (d *gateDriver) Close(context.Context) error { return nil }

func (d *gateDriver) CreatePod(context.Context, *PodSpec) (*Pod, error) { panic("unreachable") }
func (d *gateDriver) UpdatePodResources(context.Context, *Pod, *Resources) error {
	panic("unreachable")
}
func (d *gateDriver) StopPod(context.Context, *Pod) error   { panic("unreachable") }
func (d *gateDriver) RemovePod(context.Context, *Pod) error { panic("unreachable") }
func (d *gateDriver) CreateContainer(context.Context, *Pod, *ContainerSpec) (*Container, error) {
	panic("unreachable")
}
func (d *gateDriver) StartContainer(context.Context, *Container) error { panic("unreachable") }
func (d *gateDriver) UpdateContainerResources(context.Context, *Container, *Resources) error {
	panic("unreachable")
}
func (d *gateDriver) StopContainer(context.Context, *Container) error   { panic("unreachable") }
func (d *gateDriver) RemoveContainer(context.Context, *Container) error { panic("unreachable") }
func (d *gateDriver) ListRunPods(context.Context) ([]string, error)     { panic("unreachable") }
func (d *gateDriver) Cleanup(context.Context) error                     { panic("unreachable") }
func (d *gateDriver) ListRunContainers(context.Context) ([]string, error) {
	panic("unreachable")
}

var _ = Driver(&gateDriver{})

// didSkip runs gate as a subtest and reports whether the gate skipped it.
func didSkip(t *testing.T, gate func(t *testing.T)) bool {
	t.Helper()

	var skipped bool

	t.Run("gate", func(t *testing.T) {
		// Registered before the gate runs, so it also runs when Skipf unwinds the
		// subtest's goroutine.
		t.Cleanup(func() { skipped = t.Skipped() })

		gate(t)
	})

	return skipped
}

func TestRequireCapsSkipsUnlessTheDriverHasEveryRequestedCapability(t *testing.T) {
	saved := driver
	t.Cleanup(func() { driver = saved })

	driver = &gateDriver{
		name: "fake",
		caps: CapContainerLifecycle | CapObserveOpError,
	}

	for _, tc := range []struct {
		name string
		caps Capability
		skip bool
	}{
		{name: "no capability required", caps: 0, skip: false},
		{name: "one capability the driver has", caps: CapObserveOpError, skip: false},
		{
			name: "every capability the driver has",
			caps: CapContainerLifecycle | CapObserveOpError,
			skip: false,
		},
		{name: "a capability the driver lacks", caps: CapUpdatePodResources, skip: true},
		{
			name: "one capability the driver has and one it lacks",
			caps: CapContainerLifecycle | CapUpdatePodResources,
			skip: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skipped := didSkip(t, func(t *testing.T) {
				RequireCaps(t, tc.caps)
			})

			require.Equal(t, tc.skip, skipped)
		})
	}
}

// fatalGateT records what a gate reported. Fatal panics rather than returning,
// because on a real *testing.T it does not return either: a fake that fell
// through would sail straight into the nil dereference the guard exists to
// prevent, and the test would pass for the wrong reason.
type fatalGateT struct {
	skips []string
}

func (f *fatalGateT) Helper()      {}
func (f *fatalGateT) Name() string { return "fake" }

func (f *fatalGateT) Fatal(args ...any) {
	panic(fmt.Sprint(args...))
}

func (f *fatalGateT) Skipf(format string, args ...any) {
	f.skips = append(f.skips, fmt.Sprintf(format, args...))
}

var _ gateT = &fatalGateT{}

func TestRequireCapsFailsWhenSetupHasNotRun(t *testing.T) {
	// Without the nil guard this panics inside RequireCaps, which reads as a
	// framework bug rather than as the missing TestMain wiring it is. NewSuite has
	// had the same guard from the start.
	saved := driver
	t.Cleanup(func() { driver = saved })
	driver = nil

	f := &fatalGateT{}

	require.PanicsWithValue(t, "framework.Setup has not been called", func() {
		RequireCaps(f, CapContainerLifecycle)
	})
	require.Empty(t, f.skips, "a missing Setup is a failure, not a skip")
}

func TestRequireNRIVersionSkipsUnlessTheRuntimeIsAtLeastTheGivenVersion(t *testing.T) {
	saved := runtimeNRIVersion
	t.Cleanup(func() { runtimeNRIVersion = saved })

	for _, tc := range []struct {
		name string
		have string
		min  string
		skip bool
	}{
		{name: "newer than required", have: "v0.10.0", min: "v0.9.0", skip: false},
		{name: "exactly the required version", have: "v0.10.0", min: "v0.10.0", skip: false},
		{name: "older than required", have: "v0.9.0", min: "v0.10.0", skip: true},
		{name: "unknown version", have: "", min: "v0.1.0", skip: true},
		{name: "not semver", have: "unknown", min: "v0.1.0", skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeNRIVersion = tc.have

			skipped := didSkip(t, func(t *testing.T) {
				RequireNRIVersion(t, tc.min)
			})

			require.Equal(t, tc.skip, skipped)
		})
	}
}

// fakeStatusReader stands in for the Suite the allowed-list gates read a
// container's /proc/self/status through. calls is recorded so that a test can
// pin that a gate rejected its own arguments before going near a container.
type fakeStatusReader struct {
	status map[string]string
	calls  int
}

func (f *fakeStatusReader) ProcSelfStatus(*Container) map[string]string {
	f.calls++

	return f.status
}

var _ gateStatusReader = &fakeStatusReader{}

// procStatus returns a /proc/self/status carrying the given fields plus the Name
// the gates use as their positive control on the read.
func procStatus(fields map[string]string) map[string]string {
	status := map[string]string{"Name": "sleep"}
	for k, v := range fields {
		status[k] = v
	}

	return status
}

func TestSkipUnlessCPUsSkipsUnlessTheContainerIsAllowedEnoughCPUs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status map[string]string
		min    int
		skip   bool
	}{
		{
			name:   "one cpu, which makes a cpuset of 0 the default",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0"}),
			min:    2,
			skip:   true,
		},
		{
			name:   "exactly the two cpus required",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0-1"}),
			min:    2,
			skip:   false,
		},
		{
			name:   "more cpus than required",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0-43"}),
			min:    2,
			skip:   false,
		},
		{
			name:   "two cpus that are not consecutive",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0,3"}),
			min:    2,
			skip:   false,
		},
		{
			name:   "two cpus where three are required",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0-1"}),
			min:    3,
			skip:   true,
		},
		// A status with no Cpus_allowed_list is deliberately absent from this table:
		// unlike its NUMA sibling it is a failure rather than a skip, and
		// TestSkipUnlessCPUsFailsWhenTheStatusReportsNoCPUList covers it.
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeStatusReader{status: tc.status}

			skipped := didSkip(t, func(t *testing.T) {
				SkipUnlessCPUs(t, r, &Container{ID: "ctr"}, tc.min)
			})

			require.Equal(t, tc.skip, skipped)
		})
	}
}

func TestSkipUnlessNUMANodesSkipsUnlessTheContainerIsAllowedEnoughNodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status map[string]string
		min    int
		skip   bool
	}{
		{
			name:   "one node, which makes a mems of 0 the default",
			status: procStatus(map[string]string{"Mems_allowed_list": "0"}),
			min:    2,
			skip:   true,
		},
		{
			name:   "exactly the two nodes required",
			status: procStatus(map[string]string{"Mems_allowed_list": "0-1"}),
			min:    2,
			skip:   false,
		},
		{
			name:   "more nodes than required",
			status: procStatus(map[string]string{"Mems_allowed_list": "0-7"}),
			min:    2,
			skip:   false,
		},
		{
			name:   "two nodes that are not consecutive",
			status: procStatus(map[string]string{"Mems_allowed_list": "0,2"}),
			min:    2,
			skip:   false,
		},
		{
			name:   "two nodes where three are required",
			status: procStatus(map[string]string{"Mems_allowed_list": "0-1"}),
			min:    3,
			skip:   true,
		},
		{
			// A skip rather than a failure, unlike the cpu list: Mems_allowed_list is
			// printed only under CONFIG_CPUSETS, so its absence is a fact about the
			// kernel and nothing about NRI can be measured either way.
			name:   "a kernel that reports no node list at all",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0-3"}),
			min:    2,
			skip:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeStatusReader{status: tc.status}

			skipped := didSkip(t, func(t *testing.T) {
				SkipUnlessNUMANodes(t, r, &Container{ID: "ctr"}, tc.min)
			})

			require.Equal(t, tc.skip, skipped)
		})
	}
}

func TestAllowedListGatesCountTheirOwnList(t *testing.T) {
	// The two gates differ only in the /proc/self/status key they read, so reading
	// the wrong one is the mistake with no other symptom: on a host whose cpu count
	// and node count differ -- which is most hosts -- a NUMA gate that counted cpus
	// would let the single-node case through, and the vacuous ExpectMems it exists
	// to prevent would run and pass.
	status := procStatus(map[string]string{
		"Cpus_allowed_list": "0-43",
		"Mems_allowed_list": "0",
	})

	t.Run("the NUMA gate counts nodes, not cpus", func(t *testing.T) {
		skipped := didSkip(t, func(t *testing.T) {
			SkipUnlessNUMANodes(t, &fakeStatusReader{status: status}, &Container{ID: "ctr"}, 2)
		})

		require.True(t, skipped,
			"one NUMA node must skip even where 44 cpus are allowed")
	})

	t.Run("the cpu gate counts cpus, not nodes", func(t *testing.T) {
		skipped := didSkip(t, func(t *testing.T) {
			SkipUnlessCPUs(t, &fakeStatusReader{status: status}, &Container{ID: "ctr"}, 2)
		})

		require.False(t, skipped,
			"44 allowed cpus must not skip merely because there is one NUMA node")
	})
}

func TestAllowedListGatesFailWhenTheStatusWasNotRead(t *testing.T) {
	// A skip here would be the worst available outcome: a broken exec channel would
	// silently skip every gated test, and the run would be read as one that
	// measured something. See skipUnlessAllowedListSize's positive control.
	for _, tc := range []struct {
		name   string
		status map[string]string
	}{
		{name: "nothing came back", status: nil},
		{name: "an empty status", status: map[string]string{}},
		{
			name:   "a status with a list but no Name",
			status: map[string]string{"Mems_allowed_list": "0-3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fatalGateT{}
			r := &fakeStatusReader{status: tc.status}

			require.PanicsWithValue(t,
				fmt.Sprintf("the container's /proc/self/status carries no Name field, so "+
					"it was not read; NUMA nodes cannot be counted from %v", tc.status),
				func() {
					SkipUnlessNUMANodes(f, r, &Container{ID: "ctr"}, 2)
				})
			require.Empty(t, f.skips, "an unread status is a failure, not a skip")
		})
	}
}

func TestSkipUnlessCPUsFailsWhenTheStatusReportsNoCPUList(t *testing.T) {
	// The one outcome the two gates do not share. The kernel prints
	// Cpus_allowed_list for every task unconditionally, so its absence from a status
	// that was otherwise read cannot be a property of the node: something ate the
	// line, and skipping would hide a broken read behind a message about the host's
	// hardware. Mems_allowed_list needs CONFIG_CPUSETS, which is why its absence
	// skips instead -- see the NUMA table above.
	f := &fatalGateT{}
	r := &fakeStatusReader{status: procStatus(map[string]string{"Mems_allowed_list": "0-1"})}

	require.PanicsWithValue(t,
		"the container's /proc/self/status was read but reports no Cpus_allowed_list, "+
			"which the kernel prints unconditionally; its CPUs cannot be counted, and "+
			"this is a broken read rather than anything about the node. The status "+
			"carries [Mems_allowed_list Name]",
		func() {
			SkipUnlessCPUs(f, r, &Container{ID: "ctr"}, 2)
		})
	require.Empty(t, f.skips, "an absent Cpus_allowed_list is a failure, not a skip")
}

func TestAllowedListGatesFailOnAListTheyCannotParse(t *testing.T) {
	// Fail rather than skip, for the reason above: an unparsable list means the
	// count is unknown, and a gate that skipped on an unknown count would hide both
	// the vacuity it guards against and the broken read that produced it.
	//
	// The expected error text is spelled out rather than obtained by calling
	// parseAllowedList here, which would compare the parser against itself and pass
	// however the message changed.
	for _, tc := range []struct {
		name string
		list string
		err  string
	}{
		{name: "not a number", list: "zero", err: `"zero" is not a decimal number`},
		{
			name: "an empty list",
			list: "",
			err:  "the list is empty, which names no cpu or node",
		},
		{
			name: "a descending range",
			list: "3-1",
			err:  `range "3-1" descends; the kernel prints ascending ranges`,
		},
		{name: "a hexadecimal cpu", list: "0x2", err: `"0x2" is not a decimal number`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fatalGateT{}
			r := &fakeStatusReader{
				status: procStatus(map[string]string{"Mems_allowed_list": tc.list}),
			}

			require.PanicsWithValue(t,
				fmt.Sprintf("cannot read Mems_allowed_list %q from the container's "+
					"/proc/self/status, so its NUMA nodes cannot be counted: %s",
					tc.list, tc.err),
				func() {
					SkipUnlessNUMANodes(f, r, &Container{ID: "ctr"}, 2)
				})
			require.Empty(t, f.skips, "an unparsable list is a failure, not a skip")
		})
	}
}

func TestAllowedListGateSkipMessagesSayWhatWentUnmeasured(t *testing.T) {
	// The message is the entire value of an honest skip. A test that skips silently,
	// or with a message a reader cannot act on, is indistinguishable in a run's
	// output from a test that measured its subject -- which is the misreading these
	// gates exist to prevent, so the wording is pinned rather than left to drift.
	for _, tc := range []struct {
		name   string
		status map[string]string
		gate   func(gateT, gateStatusReader)
		want   []string
	}{
		{
			name:   "a single NUMA node",
			status: procStatus(map[string]string{"Mems_allowed_list": "0"}),
			gate: func(t gateT, r gateStatusReader) {
				SkipUnlessNUMANodes(t, r, &Container{ID: "ctr"}, 2)
			},
			// The line that decided it, its raw value, the minimum with its subject,
			// that something went unmeasured, and what it would take to measure it.
			want: []string{
				"Mems_allowed_list", `"0"`, "2 NUMA nodes",
				"unmeasured on this host", "2 or more NUMA nodes",
			},
		},
		{
			name:   "a single cpu",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0"}),
			gate: func(t gateT, r gateStatusReader) {
				SkipUnlessCPUs(t, r, &Container{ID: "ctr"}, 2)
			},
			want: []string{
				"Cpus_allowed_list", `"0"`, "2 CPUs",
				"unmeasured on this host", "2 or more CPUs",
			},
		},
		{
			name:   "a kernel without cpusets",
			status: procStatus(map[string]string{"Cpus_allowed_list": "0-3"}),
			gate: func(t gateT, r gateStatusReader) {
				SkipUnlessNUMANodes(t, r, &Container{ID: "ctr"}, 2)
			},
			want: []string{"reports no Mems_allowed_list", "NUMA nodes", "unmeasured"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fatalGateT{}

			tc.gate(f, &fakeStatusReader{status: tc.status})

			require.Len(t, f.skips, 1, "the gate must skip exactly once")

			for _, want := range tc.want {
				require.Contains(t, f.skips[0], want,
					"a skip message a reader cannot act on is as bad as a silent one")
			}
		})
	}
}

func TestAllowedListGatesRejectAMinimumThatCannotSkip(t *testing.T) {
	// A running container is allowed at least one cpu and one node, so a gate asking
	// for one -- or none -- can never fire. That is not a harmless argument: it is
	// the absent gate this whole mechanism exists to be, wearing a call site that
	// makes a reader believe the vacuity was handled.
	for _, min := range []int{0, 1, -1} {
		t.Run(fmt.Sprintf("a minimum of %d", min), func(t *testing.T) {
			f := &fatalGateT{}
			r := &fakeStatusReader{
				status: procStatus(map[string]string{"Mems_allowed_list": "0"}),
			}

			require.Panics(t, func() {
				SkipUnlessNUMANodes(f, r, &Container{ID: "ctr"}, min)
			})
			require.Empty(t, f.skips, "a gate that cannot skip is a failure, not a skip")
			require.Zero(t, r.calls,
				"the minimum must be rejected before a container is read")
		})
	}
}

func TestSkipUnlessConfigSuiteSkipsUnlessTheNamedSuiteWasRequested(t *testing.T) {
	saved := TestConfig.ConfigSuite
	t.Cleanup(func() { TestConfig.ConfigSuite = saved })

	for _, tc := range []struct {
		name      string
		requested string
		gate      string
		skip      bool
	}{
		{name: "no suite requested", requested: "", gate: "hugepages", skip: true},
		{name: "this suite requested", requested: "hugepages", gate: "hugepages", skip: false},
		{name: "another suite requested", requested: "devices", gate: "hugepages", skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			TestConfig.ConfigSuite = tc.requested

			skipped := didSkip(t, func(t *testing.T) {
				SkipUnlessConfigSuite(t, tc.gate)
			})

			require.Equal(t, tc.skip, skipped)
		})
	}
}

func TestSkipUnlessRuntimeHandler(t *testing.T) {
	// The gate a test asking for a specific low-level runtime needs: on a node that
	// does not have crun installed, a crun-specific test must say that what it
	// would have verified is unmeasured here rather than fail as if crun were
	// broken.
	for _, tc := range []struct {
		name       string
		discovered []string
		want       string
		skip       bool
	}{
		{name: "the handler is installed", discovered: []string{"crun", "runc"}, want: "crun", skip: false},
		{name: "the only handler installed", discovered: []string{"runc"}, want: "runc", skip: false},
		{name: "the handler is not installed", discovered: []string{"runc"}, want: "crun", skip: true},
		// A runtime that advertises no handlers at all cannot confirm it has the
		// one asked for, so the test is unverified rather than passed.
		{name: "no handlers discovered", discovered: nil, want: "crun", skip: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useRuntimeHandlers(t, tc.discovered)

			skipped := didSkip(t, func(t *testing.T) {
				SkipUnlessRuntimeHandler(t, tc.want)
			})

			require.Equal(t, tc.skip, skipped)
		})
	}

	t.Run("names the handler in the recorded skip", func(t *testing.T) {
		// The reason reaches the summary, which is read far from the test's own
		// output, so it has to name the handler that was missing and what the node
		// does have. See Skipped.
		useRuntimeHandlers(t, []string{"runc"})

		r := useReport(t)

		require.True(t, didSkip(t, func(t *testing.T) {
			SkipUnlessRuntimeHandler(t, "crun")
		}))

		got := r.Skips()
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, "crun", "the reason must name the handler asked for")
		require.Contains(t, got[0].Reason, "runc", "the reason must name what the node has")
	})
}

// requireOneRecordedSkip asserts that a gate which skipped recorded exactly one
// skip with a reason, and that a reader of the summary could act on it.
func requireOneRecordedSkip(t *testing.T, r *Report) {
	t.Helper()

	got := r.Skips()
	require.Len(t, got, 1, "the gate must record exactly one skip")
	require.NotEmpty(t, got[0].Reason, "a recorded skip must say why it skipped")

	// And which test it was. A skip with an empty Test prints as "<unknown>" in the
	// summary, which tells a reader that something went unverified and not what, so
	// there is nothing to go and run.
	require.NotEmpty(t, got[0].Test, "a recorded skip must name the test that skipped")
}

func TestGatesRecordTheirSkips(t *testing.T) {
	// A skip the summary does not name is a skip a reader takes for a pass, so
	// every gate that can skip must record it as well as skipping the test.
	t.Run("RequireCaps", func(t *testing.T) {
		saved := driver
		t.Cleanup(func() { driver = saved })
		driver = &gateDriver{name: "fake", caps: 0}

		r := useReport(t)

		skipped := didSkip(t, func(t *testing.T) {
			RequireCaps(t, CapUpdatePodResources)
		})

		require.True(t, skipped)
		requireOneRecordedSkip(t, r)
	})

	t.Run("RequireNRIVersion", func(t *testing.T) {
		saved := runtimeNRIVersion
		t.Cleanup(func() { runtimeNRIVersion = saved })
		runtimeNRIVersion = "v0.9.0"

		r := useReport(t)

		skipped := didSkip(t, func(t *testing.T) {
			RequireNRIVersion(t, "v0.10.0")
		})

		require.True(t, skipped)
		requireOneRecordedSkip(t, r)
	})

	t.Run("SkipUnlessConfigSuite", func(t *testing.T) {
		saved := TestConfig.ConfigSuite
		t.Cleanup(func() { TestConfig.ConfigSuite = saved })
		TestConfig.ConfigSuite = ""

		r := useReport(t)

		skipped := didSkip(t, func(t *testing.T) {
			SkipUnlessConfigSuite(t, "hugepages")
		})

		require.True(t, skipped)
		requireOneRecordedSkip(t, r)
	})

	t.Run("SkipUnlessCPUs", func(t *testing.T) {
		r := useReport(t)
		status := &fakeStatusReader{status: procStatus(map[string]string{"Cpus_allowed_list": "0"})}

		skipped := didSkip(t, func(t *testing.T) {
			SkipUnlessCPUs(t, status, &Container{ID: "ctr"}, 2)
		})

		require.True(t, skipped)
		requireOneRecordedSkip(t, r)
	})

	t.Run("SkipUnlessNUMANodes", func(t *testing.T) {
		r := useReport(t)
		status := &fakeStatusReader{status: procStatus(map[string]string{"Mems_allowed_list": "0"})}

		skipped := didSkip(t, func(t *testing.T) {
			SkipUnlessNUMANodes(t, status, &Container{ID: "ctr"}, 2)
		})

		require.True(t, skipped)
		requireOneRecordedSkip(t, r)
	})

	t.Run("SkipUnlessRuntimeHandler", func(t *testing.T) {
		useRuntimeHandlers(t, []string{"runc"})

		r := useReport(t)

		skipped := didSkip(t, func(t *testing.T) {
			SkipUnlessRuntimeHandler(t, "crun")
		})

		require.True(t, skipped)
		requireOneRecordedSkip(t, r)
	})
}
