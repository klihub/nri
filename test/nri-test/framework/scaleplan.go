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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/nri/pkg/api"
)

// simulatableEvents are the hooks a simulated plugin can subscribe to and delay:
// every NRI hook except container adjustment validation, which would make the
// runtime build a validation request for every container.
var simulatableEvents = []EventType{
	RunPodSandbox, UpdatePodSandbox, PostUpdatePodSandbox,
	StopPodSandbox, RemovePodSandbox,
	CreateContainer, PostCreateContainer, StartContainer, PostStartContainer,
	UpdateContainer, PostUpdateContainer, StopContainer, RemoveContainer,
}

// RecorderEvents are the events the recorder, plugin 0, subscribes to: every
// startup and teardown stage, and nothing else.
var RecorderEvents = []EventType{
	RunPodSandbox, StopPodSandbox, RemovePodSandbox,
	CreateContainer, PostCreateContainer, StartContainer, PostStartContainer,
	StopContainer, RemoveContainer,
}

// allEvents is the selector that stands for every simulatable event.
const allEvents = "all"

// delayRule is one parsed -nri-test.scale-delay entry.
type delayRule struct {
	plugin int // -1 for every plugin
	event  EventType
	all    bool
	delay  time.Duration
}

// ScalePlan describes the plugins a scalability run keeps registered: how many,
// which events each subscribes to, and how long each takes to handle each
// event. Plugin 0 is the recorder; it always subscribes to the measured stages,
// so only its delays are configurable.
type ScalePlan struct {
	// Plugins is the total plugin count, recorder included.
	Plugins int
	// Traced means the run measures from the runtime's spans. There is then no
	// recorder, and plugin 0 is a simulated plugin like the others.
	Traced bool

	events map[int][]EventType
	rules  []delayRule
}

// NewScalePlan returns the plan for plugins plugins. eventSpecs are entries of
// the form [INDEX=]EVENT[,EVENT...] and delaySpecs entries of the form
// [DURATION | [INDEX/]EVENT=DURATION][,...], with EVENT possibly "all". It
// returns an error for anything it cannot make sense of, rather than running a
// simulation that differs from what was asked for.
func NewScalePlan(plugins int, eventSpecs, delaySpecs []string) (*ScalePlan, error) {
	return newScalePlan(false, plugins, eventSpecs, delaySpecs)
}

// NewTracedScalePlan is NewScalePlan for a run measured from the runtime's
// spans. There is no recorder, so every plugin, index 0 included, is a
// simulated one whose events can be chosen.
func NewTracedScalePlan(plugins int, eventSpecs, delaySpecs []string) (*ScalePlan, error) {
	return newScalePlan(true, plugins, eventSpecs, delaySpecs)
}

func newScalePlan(traced bool, plugins int, eventSpecs, delaySpecs []string) (*ScalePlan, error) {
	if plugins < 1 {
		return nil, fmt.Errorf("-nri-test.scale-plugins must be at least 1, got %d", plugins)
	}

	p := &ScalePlan{Plugins: plugins, Traced: traced, events: map[int][]EventType{}}

	for _, spec := range eventSpecs {
		if err := p.addEvents(spec); err != nil {
			return nil, fmt.Errorf("-nri-test.scale-events %q: %w", spec, err)
		}
	}

	for _, spec := range delaySpecs {
		for _, item := range strings.Split(spec, ",") {
			if err := p.addDelay(strings.TrimSpace(item)); err != nil {
				return nil, fmt.Errorf("-nri-test.scale-delay %q: %w", spec, err)
			}
		}
	}

	return p, nil
}

// firstSimulated is the index of the first plugin whose events can be chosen.
func (p *ScalePlan) firstSimulated() int {
	if p.Traced {
		return 0
	}

	return 1
}

func (p *ScalePlan) addEvents(spec string) error {
	idx := -1
	list := spec

	if i, l, ok := strings.Cut(spec, "="); ok {
		n, err := strconv.Atoi(i)
		if err != nil {
			return fmt.Errorf("bad plugin index %q", i)
		}

		idx, list = n, l
	}

	var events []EventType

	for _, name := range strings.Split(list, ",") {
		ev, err := parseSimulatable(strings.TrimSpace(name))
		if err != nil {
			return err
		}

		events = append(events, ev)
	}

	first := p.firstSimulated()

	targets := []int{idx}
	if idx < 0 {
		targets = targets[:0]
		for i := first; i < p.Plugins; i++ {
			targets = append(targets, i)
		}

		// An event list with nothing to apply it to is refused rather than
		// ignored. Without this the loop above simply produced no targets, so
		// the flag was accepted and did nothing -- and -nri-test.scale-delay
		// already refuses the same mistake when it names an index, so the two
		// flags disagreed about it.
		if len(targets) == 0 {
			return fmt.Errorf("there are no simulated plugins to subscribe: "+
				"-nri-test.scale-plugins is %d, so the only plugin is the recorder, "+
				"whose events are fixed; raise it to add simulated plugins", p.Plugins)
		}
	}

	for _, i := range targets {
		if i < first || i >= p.Plugins {
			if p.Traced {
				return fmt.Errorf("plugin %d does not exist; indexes run 0..%d", i, p.Plugins-1)
			}

			return fmt.Errorf("plugin %d is not a simulated plugin; indexes run 1..%d "+
				"(0 is the recorder, whose events are fixed)", i, p.Plugins-1)
		}

		p.events[i] = events
	}

	return nil
}

func (p *ScalePlan) addDelay(item string) error {
	if item == "" {
		return fmt.Errorf("empty entry")
	}

	if !strings.Contains(item, "=") {
		d, err := parseDelay(item)
		if err != nil {
			return err
		}

		p.rules = append(p.rules, delayRule{plugin: -1, all: true, delay: d})

		return nil
	}

	sel, dur, _ := strings.Cut(item, "=")
	rule := delayRule{plugin: -1}

	if i, ev, ok := strings.Cut(sel, "/"); ok {
		n, err := strconv.Atoi(i)
		if err != nil || n < 0 || n >= p.Plugins {
			return fmt.Errorf("bad plugin index %q: indexes run 0..%d", i, p.Plugins-1)
		}

		rule.plugin, sel = n, ev
	}

	if sel == allEvents {
		rule.all = true
	} else {
		ev, err := parseSimulatable(sel)
		if err != nil {
			return err
		}

		rule.event = ev
	}

	d, err := parseDelay(dur)
	if err != nil {
		return err
	}

	rule.delay = d
	p.rules = append(p.rules, rule)

	return nil
}

func parseSimulatable(name string) (EventType, error) {
	for _, ev := range simulatableEvents {
		if string(ev) == name {
			return ev, nil
		}
	}

	return "", fmt.Errorf("unknown event %q: use one of %v", name, simulatableEvents)
}

func parseDelay(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q", s)
	}

	if d < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}

	return d, nil
}

// Events returns the events simulated plugin idx subscribes to. It is every
// simulatable event unless the plan says otherwise.
func (p *ScalePlan) Events(idx int) []EventType {
	if ev, ok := p.events[idx]; ok {
		return ev
	}

	return simulatableEvents
}

// Mask returns Events(idx) as a subscription mask.
func (p *ScalePlan) Mask(idx int) (api.EventMask, error) {
	names := make([]string, 0, len(p.Events(idx)))
	for _, ev := range p.Events(idx) {
		names = append(names, string(ev))
	}

	return api.ParseEventMask(names...)
}

// Delay returns how long plugin idx takes to handle ev. Later entries override
// earlier ones, so a general delay can be refined by a specific one after it.
func (p *ScalePlan) Delay(idx int, ev EventType) time.Duration {
	var d time.Duration

	for _, r := range p.rules {
		if (r.plugin == -1 || r.plugin == idx) && (r.all || r.event == ev) {
			d = r.delay
		}
	}

	return d
}

// Describe returns one line per plugin saying what it was asked to simulate, for
// a run's output to state its own configuration.
func (p *ScalePlan) Describe() []string {
	lines := make([]string, 0, p.Plugins)

	for i := 0; i < p.Plugins; i++ {
		events := RecorderEvents
		if i >= p.firstSimulated() {
			events = p.Events(i)
		}

		delays := map[EventType]time.Duration{}

		for _, ev := range simulatableEvents {
			if d := p.Delay(i, ev); d > 0 && contains(events, ev) {
				delays[ev] = d
			}
		}

		name := "recorder"
		if i >= p.firstSimulated() {
			name = fmt.Sprintf("%d events", len(events))
		}

		lines = append(lines, fmt.Sprintf("plugin %02d (%s): delays %s", i, name, formatDelays(delays)))
	}

	return lines
}

func contains(list []EventType, ev EventType) bool {
	for _, e := range list {
		if e == ev {
			return true
		}
	}

	return false
}

func formatDelays(m map[EventType]time.Duration) string {
	if len(m) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(m))
	for ev, d := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", ev, d))
	}

	sort.Strings(parts)

	return strings.Join(parts, " ")
}

// SimulateWork makes plugin idx take the plan's delay to handle each event, by
// installing a handler that waits and then returns without changes. It leaves
// events with no delay alone.
func (p *ScalePlan) SimulateWork(tp *TestPlugin, idx int) {
	hook := func(ev EventType) func(context.Context) {
		d := p.Delay(idx, ev)
		if d <= 0 {
			return nil
		}

		return func(ctx context.Context) { sleepCtx(ctx, d) }
	}

	if h := hook(RunPodSandbox); h != nil {
		tp.RunPodSandboxFn = func(ctx context.Context, _ *api.PodSandbox) error { h(ctx); return nil }
	}
	if h := hook(UpdatePodSandbox); h != nil {
		tp.UpdatePodSandboxFn = func(ctx context.Context, _ *api.PodSandbox, _, _ *api.LinuxResources) error {
			h(ctx)
			return nil
		}
	}
	if h := hook(PostUpdatePodSandbox); h != nil {
		tp.PostUpdatePodSandboxFn = func(ctx context.Context, _ *api.PodSandbox) error { h(ctx); return nil }
	}
	if h := hook(StopPodSandbox); h != nil {
		tp.StopPodSandboxFn = func(ctx context.Context, _ *api.PodSandbox) error { h(ctx); return nil }
	}
	if h := hook(RemovePodSandbox); h != nil {
		tp.RemovePodSandboxFn = func(ctx context.Context, _ *api.PodSandbox) error { h(ctx); return nil }
	}
	if h := hook(CreateContainer); h != nil {
		tp.CreateContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
			h(ctx)
			return nil, nil, nil
		}
	}
	if h := hook(PostCreateContainer); h != nil {
		tp.PostCreateContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) error { h(ctx); return nil }
	}
	if h := hook(StartContainer); h != nil {
		tp.StartContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) error { h(ctx); return nil }
	}
	if h := hook(PostStartContainer); h != nil {
		tp.PostStartContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) error { h(ctx); return nil }
	}
	if h := hook(UpdateContainer); h != nil {
		tp.UpdateContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container, _ *api.LinuxResources) ([]*api.ContainerUpdate, error) {
			h(ctx)
			return nil, nil
		}
	}
	if h := hook(PostUpdateContainer); h != nil {
		tp.PostUpdateContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) error { h(ctx); return nil }
	}
	if h := hook(StopContainer); h != nil {
		tp.StopContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) ([]*api.ContainerUpdate, error) {
			h(ctx)
			return nil, nil
		}
	}
	if h := hook(RemoveContainer); h != nil {
		tp.RemoveContainerFn = func(ctx context.Context, _ *api.PodSandbox, _ *api.Container) error { h(ctx); return nil }
	}
}

// sleepCtx waits for d, or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
