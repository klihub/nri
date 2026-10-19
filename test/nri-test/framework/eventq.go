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
	"sync"
	"time"

	"github.com/containerd/nri/pkg/api"
)

// EventType names an NRI callback.
type EventType string

// The NRI callbacks a plugin can be invoked for. The string values match the
// event names api.MustParseEventMask accepts, so a subscription mask and the
// events it produces can be written with the same identifiers.
//
// The first group are plugin lifecycle events rather than NRI hooks: nothing
// subscribes to them and they are not driven by a workload. If you add one, add
// it to nonHookEventTypes below too, or IsHook will call it a hook and every
// negative assertion in the suite will start reporting it as an event the plugin
// received without subscribing to it.
const (
	Configured   EventType = "Configured"
	Synchronized EventType = "Synchronized"
	Shutdown     EventType = "Shutdown"
	Disconnected EventType = "Disconnected"
	Stopped      EventType = "Stopped"
	// UpdateDropped records that an update a test returned was discarded because
	// it named a container outside this run. It is deliberately loud: silently
	// dropping would hide a test bug, and not dropping would mutate a workload we
	// do not own.
	UpdateDropped EventType = "UpdateDropped"

	RunPodSandbox        EventType = "RunPodSandbox"
	UpdatePodSandbox     EventType = "UpdatePodSandbox"
	PostUpdatePodSandbox EventType = "PostUpdatePodSandbox"
	StopPodSandbox       EventType = "StopPodSandbox"
	RemovePodSandbox     EventType = "RemovePodSandbox"

	CreateContainer     EventType = "CreateContainer"
	PostCreateContainer EventType = "PostCreateContainer"
	StartContainer      EventType = "StartContainer"
	PostStartContainer  EventType = "PostStartContainer"
	UpdateContainer     EventType = "UpdateContainer"
	PostUpdateContainer EventType = "PostUpdateContainer"
	StopContainer       EventType = "StopContainer"
	RemoveContainer     EventType = "RemoveContainer"

	ValidateContainerAdjustment EventType = "ValidateContainerAdjustment"
)

// nonHookEventTypes are the event types a TestPlugin records about itself rather
// than because the runtime invoked an NRI hook.
var nonHookEventTypes = []EventType{
	Configured,
	Synchronized,
	Shutdown,
	Disconnected,
	Stopped,
	UpdateDropped,
}

// IsHook reports whether t records an NRI hook invocation, as opposed to one of
// the plugin lifecycle events the framework records for itself.
//
// A negative assertion -- "the plugin received nothing it did not subscribe to" --
// must exempt the lifecycle events, because they are not subscribable. Listing
// them by hand in each such assertion is what left Shutdown and Disconnected out
// of the one in TestEventSubscriptions. Disconnected is the dangerous omission:
// it is recorded from the stub's WithOnClose callback, which ttrpc also fires on
// an explicit Stop, from an unsynchronised goroutine, so it can be appended at
// any point after a plugin is stopped.
func (t EventType) IsHook() bool {
	return !slices.Contains(nonHookEventTypes, t)
}

// Event records one NRI callback.
type Event struct {
	// Type is the callback this event records.
	Type EventType
	// Plugin is the full name of the plugin that recorded the event.
	Plugin string
	// PodID and PodName identify the pod, when the callback has one.
	PodID   string
	PodName string
	// CtrID and CtrName identify the container, when the callback has one.
	CtrID   string
	CtrName string
	// At is when the event was recorded.
	At time.Time
}

// PodEvent returns an event for a pod callback.
func PodEvent(plugin string, pod *api.PodSandbox, t EventType) *Event {
	return &Event{
		Type:    t,
		Plugin:  plugin,
		PodID:   pod.GetId(),
		PodName: pod.GetName(),
		At:      time.Now(),
	}
}

// CtrEvent returns an event for a container callback.
func CtrEvent(plugin string, pod *api.PodSandbox, ctr *api.Container, t EventType) *Event {
	return &Event{
		Type:    t,
		Plugin:  plugin,
		PodID:   pod.GetId(),
		PodName: pod.GetName(),
		CtrID:   ctr.GetId(),
		CtrName: ctr.GetName(),
		At:      time.Now(),
	}
}

// Matches reports whether e satisfies the o. Empty fields in o are wildcards,
// so a bare &Event{Type: StartContainer} matches any StartContainer event.
func (e *Event) Matches(o *Event) bool {
	if e.Type != o.Type {
		return false
	}
	if o.Plugin != "" && e.Plugin != o.Plugin {
		return false
	}
	if o.PodID != "" && e.PodID != o.PodID {
		return false
	}
	if o.CtrID != "" && e.CtrID != o.CtrID {
		return false
	}

	return true
}

func (e *Event) String() string {
	str := e.Plugin + ":"
	switch {
	case e.CtrID != "":
		str += e.PodID + "/" + e.CtrID + "/"
	case e.PodID != "":
		str += e.PodID + "/"
	}

	return str + string(e.Type)
}

// EventQ records events and lets tests wait for them.
//
// Add is called from plugin callback goroutines and must never block, so waiters
// are woken through a broadcast channel that Add closes and clears under the
// lock, rather than by sending each event to a waiter.
//
// The queue is deliberately append-only: there is no way to clear it. Suite.Q
// hands the run's queue to every case, and Suite.cleanup's dropped-update guard
// is a scan of that queue, so a case able to empty it could erase the record of
// its own silently emptied update and still pass. A Reset method existed for
// exactly one test and offered exactly that, which is why it was removed along
// with the AllowDroppedUpdates opt-out. A case that genuinely needs a fresh queue
// must construct its own.
type EventQ struct {
	mu      sync.Mutex
	q       []*Event
	changed chan struct{}

	// trackCost makes Add record how long each event took to reach the queue.
	// Off by default: it is only wanted by the scalability measurement, which
	// has to be able to say how much of what it measured was its own cost.
	trackCost bool
	costs     []time.Duration
}

// Grow reserves room for n more events.
//
// The scalability measurement calls this before a burst. Add runs inside NRI
// callbacks, which the runtime waits for, so an append that reallocates adds
// its cost to the latency being measured.
func (q *EventQ) Grow(n int) {
	if q == nil || n <= 0 {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.q = slices.Grow(q.q, n)

	if q.trackCost {
		q.costs = slices.Grow(q.costs, n)
	}
}

// TrackCost turns per-event cost recording on or off, and reserves room for n
// samples.
func (q *EventQ) TrackCost(on bool, n int) {
	if q == nil {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.trackCost = on

	if on && n > 0 {
		q.costs = slices.Grow(q.costs, n)
	}
}

// Costs returns how long each recorded event took to get from the timestamp
// its caller took to being queued.
//
// This is the instrument's own cost, and it is a lower bound on it: it spans
// the scope filter and the queue's lock and append, not the NRI transport
// before the callback nor the callback's return afterwards. A scalability
// number whose instrument sits on the critical path has to be able to say how
// big the instrument is, and this is the part of it we can see.
func (q *EventQ) Costs() []time.Duration {
	if q == nil {
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	return slices.Clone(q.costs)
}

// Add records an event and wakes any waiters.
func (q *EventQ) Add(e *Event) {
	if q == nil {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.q = append(q.q, e)

	if q.trackCost && e != nil && !e.At.IsZero() {
		q.costs = append(q.costs, time.Since(e.At))
	}

	if q.changed != nil {
		close(q.changed)
		q.changed = nil
	}
}

// notify returns the channel closed on the next Add. The caller must hold the
// lock.
func (q *EventQ) notify() chan struct{} {
	if q.changed == nil {
		q.changed = make(chan struct{})
	}

	return q.changed
}

// Events returns all recorded events, in recording order.
func (q *EventQ) Events() []*Event {
	q.mu.Lock()
	defer q.mu.Unlock()

	return append([]*Event{}, q.q...)
}

// EventsFor returns the events recorded by the named plugin.
func (q *EventQ) EventsFor(plugin string) []*Event {
	var events []*Event
	for _, e := range q.Events() {
		if e.Plugin == plugin {
			events = append(events, e)
		}
	}

	return events
}

// Has reports whether a matching event has been recorded.
func (q *EventQ) Has(e *Event) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.search(e) != nil
}

func (q *EventQ) search(e *Event) *Event {
	for _, qe := range q.q {
		if qe.Matches(e) {
			return qe
		}
	}

	return nil
}

// WaitFor waits up to timeout for a matching event.
func (q *EventQ) WaitFor(w *Event, timeout time.Duration) (*Event, error) {
	return q.Wait(w, time.After(timeout))
}

// Wait waits for a matching event until deadline fires. Several goroutines may
// wait at once.
func (q *EventQ) Wait(w *Event, deadline <-chan time.Time) (*Event, error) {
	for {
		q.mu.Lock()
		if e := q.search(w); e != nil {
			q.mu.Unlock()

			return e, nil
		}

		// Re-arm before releasing the lock, so an Add between the search above
		// and the select below still wakes us.
		changed := q.notify()
		q.mu.Unlock()

		select {
		case <-changed:
		case <-deadline:
			return nil, fmt.Errorf("timed out waiting for event %s", w)
		}
	}
}
