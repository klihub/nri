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
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Scalability from the runtime's own OpenTelemetry spans.
//
// A runtime that traces its NRI calls emits one client span per plugin per
// hook, named after the hook. That is the runtime's view of the same thing the
// recorder plugin measures from the plugin's side, and it needs no measuring
// plugin registered: the plugins in a run can all be simulated ones.
//
// A hook span carries no pod or container identity of its own. The identity is
// on the spans above it, the CRI call and the runtime's sandbox and container
// work, so a hook is attributed by walking up its parents.

const (
	// nriPluginService is the rpc.service of the runtime-to-plugin calls.
	nriPluginService = "nri.pkg.api.v1alpha1.Plugin"
	// maxAncestors bounds the walk up a span's parents.
	maxAncestors = 32
)

// HookCall is one runtime-to-plugin call, from a span.
type HookCall struct {
	// Hook is the NRI hook that was called.
	Hook EventType
	// PodName and PodID identify the pod the call was about.
	PodName, PodID string
	// CtrID identifies the container, for container hooks.
	CtrID string
	// Start and End bracket the call as the runtime saw it, which includes the
	// plugin's processing time and the ttrpc round trip.
	Start, End time.Time
	// Seq is this call's position among the plugin calls for the same hook and
	// target, in start order. NRI calls plugins in index order, so it is the
	// plugin's position, but the spans do not say so themselves.
	Seq int
}

// Duration returns how long the call took.
func (c *HookCall) Duration() time.Duration { return c.End.Sub(c.Start) }

// HookTrace is the NRI activity recovered from a set of spans.
type HookTrace struct {
	// Calls are the attributed plugin calls, ordered by start.
	Calls []*HookCall
	// Events are the calls reduced to what a recorder would have produced: the
	// first call's start for each hook and target, ordered by time. Plugin is
	// "trace".
	Events []*Event
	// Orphans are the hook calls that could not be tied to a pod, in start
	// order. containerd starts its StopContainer span as a root, with nothing
	// above it to say which container it was for.
	Orphans []*HookCall
}

func isHookSpan(s *TraceSpan) (EventType, bool) {
	method := s.Attrs["rpc.method"]

	if s.Attrs["rpc.service"] != nriPluginService {
		name, ok := strings.CutPrefix(s.Name, nriPluginService+"/")
		if !ok {
			return "", false
		}

		method = name
	}

	ev := EventType(method)

	for _, h := range simulatableEvents {
		if h == ev {
			return ev, true
		}
	}

	return "", false
}

// NewHookTrace attributes the NRI hook spans among spans to pods.
//
// A hook's pod is the sandbox id found on its nearest ancestor that has one,
// and its container the container id on its nearest ancestor that has one, for
// the hooks that are about a container. A sandbox id becomes a pod name through
// the sandbox name some span carries, or through known, which maps sandbox ids
// to names for callers that already have them. Some runtimes put no ids on
// their spans at all, so traces maps the trace ids of calls the client
// started itself, with a trace context it chose, to the pod they were for.
func NewHookTrace(spans []*TraceSpan, known, traces map[string]string) *HookTrace {
	byID := make(map[string]*TraceSpan, len(spans))
	for _, s := range spans {
		byID[s.Trace+"/"+s.ID] = s
	}

	// sandbox id -> pod name, and container id -> sandbox id, from any span
	// that has the pair.
	podOf := map[string]string{}
	for id, name := range known {
		podOf[id] = name
	}

	sandboxOf := map[string]string{}

	for _, s := range spans {
		sb := s.Attrs["sandbox.id"]
		if sb == "" {
			continue
		}

		if name := s.Attrs["sandbox.name"]; name != "" {
			if _, ok := podOf[sb]; !ok {
				podOf[sb], _, _ = strings.Cut(name, "_")
			}
		}

		if c := s.Attrs["container.id"]; c != "" {
			sandboxOf[c] = sb
		}
	}

	ht := &HookTrace{}

	for _, s := range spans {
		hook, ok := isHookSpan(s)
		if !ok {
			continue
		}

		var sandbox, ctr string

		cur := s
		for i := 0; i < maxAncestors && cur != nil; i++ {
			if sandbox == "" {
				sandbox = cur.Attrs["sandbox.id"]
			}

			if ctr == "" && !IsPodLevel(hook) {
				ctr = cur.Attrs["container.id"]
			}

			if cur.Parent == "" {
				break
			}

			cur = byID[cur.Trace+"/"+cur.Parent]
		}

		if sandbox == "" && ctr != "" {
			sandbox = sandboxOf[ctr]
		}

		name := podOf[sandbox]
		if name == "" {
			name = traces[s.Trace]
		}

		if name == "" {
			ht.Orphans = append(ht.Orphans, &HookCall{Hook: hook, Start: s.Start, End: s.End})

			continue
		}

		ht.Calls = append(ht.Calls, &HookCall{
			Hook: hook, PodName: name, PodID: sandbox, CtrID: ctr,
			Start: s.Start, End: s.End,
		})
	}

	sort.SliceStable(ht.Calls, func(i, j int) bool { return ht.Calls[i].Start.Before(ht.Calls[j].Start) })
	sort.SliceStable(ht.Orphans, func(i, j int) bool { return ht.Orphans[i].Start.Before(ht.Orphans[j].Start) })

	type key struct {
		hook EventType
		pod  string
		ctr  string
	}

	seen := map[key]int{}

	for _, c := range ht.Calls {
		k := key{c.Hook, c.PodName, c.CtrID}
		c.Seq = seen[k]
		seen[k]++

		if c.Seq > 0 {
			continue
		}

		ht.Events = append(ht.Events, &Event{
			Type: c.Hook, Plugin: "trace",
			PodID: c.PodID, PodName: c.PodName, CtrID: c.CtrID,
			At: c.Start,
		})
	}

	return ht
}

// HookStat summarizes one hook's calls.
type HookStat struct {
	// Hook is the NRI hook.
	Hook EventType
	// Call is the distribution of single plugin calls, each as the runtime saw
	// it.
	Call Stat
	// Orphan is the distribution of calls to this hook that could not be tied
	// to a pod but fell inside the window of those that could. Left out of
	// Call and Chain, which are per pod; N is zero when there were none.
	Orphan Stat
	// Chain is the distribution of the whole hook per pod or container: from
	// the first plugin being called to the last one answering. With one plugin
	// it equals Call.
	Chain Stat
}

// HookStats summarizes the calls to the named pods, restricted to the given
// hooks, one entry per hook in the order a pod meets them.
func (ht *HookTrace) HookStats(names []string, hooks []EventType) []HookStat {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}

	type key struct {
		hook EventType
		pod  string
		ctr  string
	}

	type span struct{ first, last time.Time }

	calls := map[EventType][]time.Duration{}
	chains := map[key]*span{}

	var from, to time.Time

	for _, c := range ht.Calls {
		if _, ok := want[c.PodName]; !ok {
			continue
		}

		// The window takes in every hook of the pods, not only the ones being
		// summarized: a pod's StopContainer fires before its first teardown
		// hook that can be attributed, and is the one that goes missing.
		if from.IsZero() || c.Start.Before(from) {
			from = c.Start
		}

		if c.End.After(to) {
			to = c.End
		}

		if !slices.Contains(hooks, c.Hook) {
			continue
		}

		calls[c.Hook] = append(calls[c.Hook], c.Duration())

		k := key{c.Hook, c.PodName, c.CtrID}
		if sp, ok := chains[k]; ok {
			if c.End.After(sp.last) {
				sp.last = c.End
			}

			continue
		}

		chains[k] = &span{first: c.Start, last: c.End}
	}

	chainDur := map[EventType][]time.Duration{}
	for k, sp := range chains {
		chainDur[k.hook] = append(chainDur[k.hook], sp.last.Sub(sp.first))
	}

	// The window is the pods' own attributed calls, so an orphan from before
	// the first of them, or after the last, is left out.
	orphans := map[EventType][]time.Duration{}

	for _, o := range ht.Orphans {
		if slices.Contains(hooks, o.Hook) && !from.IsZero() && !o.Start.Before(from) && !o.End.After(to) {
			orphans[o.Hook] = append(orphans[o.Hook], o.Duration())
		}
	}

	var out []HookStat

	for _, h := range hookOrder {
		if len(calls[h]) == 0 && len(orphans[h]) == 0 {
			continue
		}

		out = append(out, HookStat{
			Hook:   h,
			Call:   NewStat(string(h)+" call", calls[h]),
			Chain:  NewStat(string(h)+" chain", chainDur[h]),
			Orphan: NewStat(string(h)+" unattributed", orphans[h]),
		})
	}

	return out
}

// hookOrder is the order hooks are listed in, up and then down.
var hookOrder = []EventType{
	RunPodSandbox, CreateContainer, PostCreateContainer, StartContainer,
	PostStartContainer, StopContainer, RemoveContainer, StopPodSandbox,
	RemovePodSandbox,
}

// WriteHookStats renders the runtime's view of its NRI calls.
func WriteHookStats(b *strings.Builder, stats []HookStat) {
	if len(stats) == 0 {
		return
	}

	fmt.Fprintf(b, "\n  NRI calls as the runtime's spans saw them:\n\n")
	fmt.Fprintf(b, "    %-22s %-7s %5s %9s %9s %9s\n", "hook", "scope", "n", "p50", "p90", "max")

	for _, s := range stats {
		name := string(s.Hook)

		for _, row := range []struct {
			scope string
			st    Stat
		}{{"call", s.Call}, {"chain", s.Chain}, {"orphan", s.Orphan}} {
			if row.st.N == 0 {
				continue
			}

			fmt.Fprintf(b, "    %-22s %-7s %5d %9s %9s %9s\n",
				name, row.scope, row.st.N, row.st.P50, row.st.P90, row.st.Max)

			name = ""
		}
	}

	fmt.Fprintf(b, "\n    \"call\" is one plugin's round trip; \"chain\" is every plugin\n")
	fmt.Fprintf(b, "    for one pod or container, first call to last answer. \"orphan\"\n")
	fmt.Fprintf(b, "    calls are ones the runtime's spans do not tie to a pod.\n")
}
