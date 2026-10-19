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
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	nrilog "github.com/containerd/nri/pkg/log"
	"github.com/containerd/nri/pkg/stub"
)

// scopeGuard describes one scope-filtered TestPlugin callback, so that the guard
// on every one of them is exercised by the same table.
type scopeGuard struct {
	// field is the name of the TestPlugin handler field install assigns. The
	// coverage subtest of TestTestPluginScopeGuards reflects over TestPlugin and
	// fails if a handler field has no row here, so a callback cannot be added
	// without its guard being tested.
	field string
	// event is the event the callback records for an in-scope pod.
	event EventType
	// install assigns the callback's handler. The handler increments *calls and,
	// where the signature allows, returns a non-zero result and a non-nil error,
	// so an out-of-scope invocation that reached it is visible in the callback's
	// return values as well as in the counter.
	install func(p *TestPlugin, calls *int)
	// invoke calls the callback with pod, and reports whether any of its return
	// values was non-zero alongside the error it returned.
	invoke func(p *TestPlugin, pod *api.PodSandbox) (bool, error)
}

// errHandlerRan is what a scopeGuard handler returns, so that a guard which let
// an out-of-scope workload through is visible in the callback's error too.
var errHandlerRan = errors.New("handler ran")

// scopeGuards returns a row for every scope-filtered TestPlugin callback.
func scopeGuards() []scopeGuard {
	var (
		ctx     = context.Background()
		updates = []*api.ContainerUpdate{{ContainerId: "ctr0", Linux: &api.LinuxContainerUpdate{}}}
	)

	// podHandler and ctrHandler build the two common handler shapes: count the
	// call and fail, so both the counter and the error reveal a bypassed guard.
	podHandler := func(calls *int) func(context.Context, *api.PodSandbox) error {
		return func(context.Context, *api.PodSandbox) error {
			*calls++

			return errHandlerRan
		}
	}
	ctrHandler := func(calls *int) func(context.Context, *api.PodSandbox, *api.Container) error {
		return func(context.Context, *api.PodSandbox, *api.Container) error {
			*calls++

			return errHandlerRan
		}
	}

	return []scopeGuard{
		{
			field: "RunPodSandboxFn",
			event: RunPodSandbox,
			install: func(p *TestPlugin, calls *int) {
				p.RunPodSandboxFn = podHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.RunPodSandbox(ctx, pod)
			},
		},
		{
			field: "UpdatePodSandboxFn",
			event: UpdatePodSandbox,
			install: func(p *TestPlugin, calls *int) {
				p.UpdatePodSandboxFn = func(context.Context, *api.PodSandbox, *api.LinuxResources, *api.LinuxResources) error {
					*calls++

					return errHandlerRan
				}
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.UpdatePodSandbox(ctx, pod, nil, nil)
			},
		},
		{
			field: "PostUpdatePodSandboxFn",
			event: PostUpdatePodSandbox,
			install: func(p *TestPlugin, calls *int) {
				p.PostUpdatePodSandboxFn = podHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.PostUpdatePodSandbox(ctx, pod)
			},
		},
		{
			field: "StopPodSandboxFn",
			event: StopPodSandbox,
			install: func(p *TestPlugin, calls *int) {
				p.StopPodSandboxFn = podHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.StopPodSandbox(ctx, pod)
			},
		},
		{
			field: "RemovePodSandboxFn",
			event: RemovePodSandbox,
			install: func(p *TestPlugin, calls *int) {
				p.RemovePodSandboxFn = podHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.RemovePodSandbox(ctx, pod)
			},
		},
		{
			field: "CreateContainerFn",
			event: CreateContainer,
			install: func(p *TestPlugin, calls *int) {
				p.CreateContainerFn = func(context.Context, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
					*calls++
					a := &api.ContainerAdjustment{}
					a.AddAnnotation("must-not", "appear")

					return a, updates, errHandlerRan
				}
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				adjust, update, err := p.CreateContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))

				return adjust != nil || update != nil, err
			},
		},
		{
			field: "PostCreateContainerFn",
			event: PostCreateContainer,
			install: func(p *TestPlugin, calls *int) {
				p.PostCreateContainerFn = ctrHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.PostCreateContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))
			},
		},
		{
			field: "StartContainerFn",
			event: StartContainer,
			install: func(p *TestPlugin, calls *int) {
				p.StartContainerFn = ctrHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.StartContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))
			},
		},
		{
			field: "PostStartContainerFn",
			event: PostStartContainer,
			install: func(p *TestPlugin, calls *int) {
				p.PostStartContainerFn = ctrHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.PostStartContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))
			},
		},
		{
			field: "UpdateContainerFn",
			event: UpdateContainer,
			install: func(p *TestPlugin, calls *int) {
				p.UpdateContainerFn = func(context.Context, *api.PodSandbox, *api.Container, *api.LinuxResources) ([]*api.ContainerUpdate, error) {
					*calls++

					return updates, errHandlerRan
				}
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				update, err := p.UpdateContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"), nil)

				return update != nil, err
			},
		},
		{
			field: "PostUpdateContainerFn",
			event: PostUpdateContainer,
			install: func(p *TestPlugin, calls *int) {
				p.PostUpdateContainerFn = ctrHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.PostUpdateContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))
			},
		},
		{
			field: "StopContainerFn",
			event: StopContainer,
			install: func(p *TestPlugin, calls *int) {
				p.StopContainerFn = func(context.Context, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error) {
					*calls++

					return updates, errHandlerRan
				}
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				update, err := p.StopContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))

				return update != nil, err
			},
		},
		{
			field: "RemoveContainerFn",
			event: RemoveContainer,
			install: func(p *TestPlugin, calls *int) {
				p.RemoveContainerFn = ctrHandler(calls)
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.RemoveContainer(ctx, pod, testCtr(pod.GetId(), "ctr0"))
			},
		},
		{
			field: "ValidateAdjustmentFn",
			event: ValidateContainerAdjustment,
			install: func(p *TestPlugin, calls *int) {
				p.ValidateAdjustmentFn = func(context.Context, *api.ValidateContainerAdjustmentRequest) error {
					*calls++

					return errHandlerRan
				}
			},
			invoke: func(p *TestPlugin, pod *api.PodSandbox) (bool, error) {
				return false, p.ValidateContainerAdjustment(ctx, &api.ValidateContainerAdjustmentRequest{
					Pod:       pod,
					Container: testCtr(pod.GetId(), "ctr0"),
				})
			},
		},
	}
}

// TestTestPluginScopeGuards checks the scope guard on every scope-filtered
// callback, not just the handful a test happens to use. The guard is the suite's
// top safety property: a callback added without it, or a guard dropped in a
// refactor, would let the suite adjust, fail or delay a production workload.
func TestTestPluginScopeGuards(t *testing.T) {
	guards := scopeGuards()

	newPlugin := func() (*TestPlugin, *EventQ, *int) {
		var (
			q     = &EventQ{}
			calls int
		)

		return NewTestPlugin("test", "00", 0, q, &Scope{
			Namespaces: []string{"nri-test"},
			RunID:      "run0",
		}), q, &calls
	}

	for _, g := range guards {
		t.Run("should ignore an out-of-scope pod in "+string(g.event), func(t *testing.T) {
			p, q, calls := newPlugin()
			g.install(p, calls)

			nonZero, err := g.invoke(p, scopedPod("pod0", "default", "run0"))

			require.NoError(t, err, "guard let the handler's error through")
			require.False(t, nonZero, "guard let the handler's result through")
			require.Zero(t, *calls, "guard let the handler run")
			require.Empty(t, q.Events(), "guard recorded an event for a foreign pod")
		})

		t.Run("should invoke the handler for an in-scope pod in "+string(g.event), func(t *testing.T) {
			// The mirror of the above: a guard inverted to reject everything
			// would pass every out-of-scope case and fail here.
			p, q, calls := newPlugin()
			g.install(p, calls)

			_, err := g.invoke(p, scopedPod("pod0", "nri-test", "run0"))

			require.ErrorIs(t, err, errHandlerRan)
			require.Equal(t, 1, *calls)
			require.True(t, q.Has(&Event{Type: g.event, Plugin: "00-test"}))
		})
	}

	t.Run("should have a table row for every scope-filtered callback", func(t *testing.T) {
		// Reflect over TestPlugin so a callback cannot be added without a row
		// above: the new handler field has no coverage and this fails.
		covered := map[string]bool{}
		for _, g := range guards {
			require.False(t, covered[g.field], "duplicate table row for %s", g.field)
			covered[g.field] = true
		}

		// SynchronizeFn has no row here because its filter has a different shape:
		// it is the handshake, invoked once with node-wide lists, so it is filtered
		// per entry rather than invoked-or-not. TestTestPluginFiltersEveryExit
		// covers that.
		notFiltered := map[string]bool{"SynchronizeFn": true}

		typ := reflect.TypeOf(TestPlugin{})
		for i := range typ.NumField() {
			name := typ.Field(i).Name
			if !strings.HasSuffix(name, "Fn") || notFiltered[name] {
				continue
			}

			require.True(t, covered[name],
				"TestPlugin.%s has no scopeGuards row; add one so its scope guard is tested", name)
		}

		require.Len(t, covered, 14, "NRI has 14 scope-filtered callbacks")
	})
}

func TestTestPluginScopeFiltering(t *testing.T) {
	newPlugin := func() (*TestPlugin, *EventQ, *int) {
		var (
			q      = &EventQ{}
			calls  int
			plugin = NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		)

		plugin.RunPodSandboxFn = func(context.Context, *api.PodSandbox) error {
			calls++

			return nil
		}

		return plugin, q, &calls
	}

	t.Run("should record and invoke for an in-scope pod", func(t *testing.T) {
		plugin, q, calls := newPlugin()

		require.NoError(t, plugin.RunPodSandbox(context.Background(), scopedPod("pod0", "nri-test", "run0")))
		require.Equal(t, 1, *calls)
		require.True(t, q.Has(&Event{Type: RunPodSandbox}))
	})

	t.Run("should ignore an out-of-scope pod entirely", func(t *testing.T) {
		plugin, q, calls := newPlugin()

		require.NoError(t, plugin.RunPodSandbox(context.Background(), scopedPod("pod0", "default", "run0")))
		require.Equal(t, 0, *calls)
		require.Empty(t, q.Events())
	})

	t.Run("should not adjust an out-of-scope container", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.CreateContainerFn = func(context.Context, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
			a := &api.ContainerAdjustment{}
			a.AddAnnotation("should-not", "appear")

			return a, nil, nil
		}

		adjust, update, err := plugin.CreateContainer(
			context.Background(),
			scopedPod("pod0", "default", "run0"),
			testCtr("pod0", "ctr0"),
		)
		require.NoError(t, err)
		require.Nil(t, adjust)
		require.Nil(t, update)
		require.Empty(t, q.Events())
	})

	t.Run("should adjust an in-scope container", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.CreateContainerFn = func(context.Context, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
			a := &api.ContainerAdjustment{}
			a.AddAnnotation("test-key", "test-value")

			return a, nil, nil
		}

		adjust, update, err := plugin.CreateContainer(
			context.Background(),
			scopedPod("pod0", "nri-test", "run0"),
			testCtr("pod0", "ctr0"),
		)
		require.NoError(t, err)
		require.NotNil(t, adjust)
		require.Equal(t, "test-value", adjust.GetAnnotations()["test-key"])
		require.Nil(t, update)
		require.True(t, q.Has(&Event{Type: CreateContainer}))
	})

	t.Run("should not reject an out-of-scope container adjustment", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.ValidateAdjustmentFn = func(context.Context, *api.ValidateContainerAdjustmentRequest) error {
			return errors.New("must not be consulted")
		}

		err := plugin.ValidateContainerAdjustment(
			context.Background(),
			&api.ValidateContainerAdjustmentRequest{
				Pod:       scopedPod("pod0", "default", "run0"),
				Container: testCtr("pod0", "ctr0"),
			},
		)
		require.NoError(t, err)
		require.Empty(t, q.Events())
	})

	t.Run("should update an in-scope container", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.UpdateContainerFn = func(context.Context, *api.PodSandbox, *api.Container, *api.LinuxResources) ([]*api.ContainerUpdate, error) {
			return []*api.ContainerUpdate{
				{ContainerId: "ctr0", Linux: &api.LinuxContainerUpdate{}},
			}, nil
		}

		updates, err := plugin.UpdateContainer(
			context.Background(),
			scopedPod("pod0", "nri-test", "run0"),
			testCtr("pod0", "ctr0"),
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, updates)
		require.Len(t, updates, 1)
		require.True(t, q.Has(&Event{Type: UpdateContainer}))
	})

	t.Run("should not update an out-of-scope container", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.UpdateContainerFn = func(context.Context, *api.PodSandbox, *api.Container, *api.LinuxResources) ([]*api.ContainerUpdate, error) {
			return []*api.ContainerUpdate{
				{ContainerId: "ctr0", Linux: &api.LinuxContainerUpdate{}},
			}, nil
		}

		updates, err := plugin.UpdateContainer(
			context.Background(),
			scopedPod("pod0", "default", "run0"),
			testCtr("pod0", "ctr0"),
			nil,
		)
		require.NoError(t, err)
		require.Nil(t, updates)
		require.Empty(t, q.Events())
	})

	t.Run("should stop an in-scope container", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.StopContainerFn = func(context.Context, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error) {
			return []*api.ContainerUpdate{
				{ContainerId: "ctr0", Linux: &api.LinuxContainerUpdate{}},
			}, nil
		}

		updates, err := plugin.StopContainer(
			context.Background(),
			scopedPod("pod0", "nri-test", "run0"),
			testCtr("pod0", "ctr0"),
		)
		require.NoError(t, err)
		require.NotNil(t, updates)
		require.Len(t, updates, 1)
		require.True(t, q.Has(&Event{Type: StopContainer}))
	})

	t.Run("should not stop an out-of-scope container", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{Namespaces: []string{"nri-test"}, RunID: "run0"})
		plugin.StopContainerFn = func(context.Context, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error) {
			return []*api.ContainerUpdate{
				{ContainerId: "ctr0", Linux: &api.LinuxContainerUpdate{}},
			}, nil
		}

		updates, err := plugin.StopContainer(
			context.Background(),
			scopedPod("pod0", "default", "run0"),
			testCtr("pod0", "ctr0"),
		)
		require.NoError(t, err)
		require.Nil(t, updates)
		require.Empty(t, q.Events())
	})
}

func TestTestPluginConfigure(t *testing.T) {
	t.Run("validation bit set, ValidateAdjustmentFn non-nil should retain validation bit", func(t *testing.T) {
		q := &EventQ{}
		var mask stub.EventMask
		mask.Set(api.Event_VALIDATE_CONTAINER_ADJUSTMENT)
		plugin := NewTestPlugin("test", "00", mask, q, &Scope{})
		plugin.ValidateAdjustmentFn = func(context.Context, *api.ValidateContainerAdjustmentRequest) error {
			return nil
		}

		returned, err := plugin.Configure(context.Background(), "", "runtime", "1.0")
		require.NoError(t, err)
		require.True(t, returned.IsSet(api.Event_VALIDATE_CONTAINER_ADJUSTMENT))
		require.NotZero(t, returned)
	})

	t.Run("validation bit set, ValidateAdjustmentFn nil should clear validation bit", func(t *testing.T) {
		q := &EventQ{}
		var mask stub.EventMask
		mask.Set(api.Event_VALIDATE_CONTAINER_ADJUSTMENT)
		plugin := NewTestPlugin("test", "00", mask, q, &Scope{})
		plugin.ValidateAdjustmentFn = nil

		returned, err := plugin.Configure(context.Background(), "", "runtime", "1.0")
		require.NoError(t, err)
		require.False(t, returned.IsSet(api.Event_VALIDATE_CONTAINER_ADJUSTMENT))
		require.NotZero(t, returned)
	})

	t.Run("zero mask should return minimalEventMask, not zero", func(t *testing.T) {
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{})

		returned, err := plugin.Configure(context.Background(), "", "runtime", "1.0")
		require.NoError(t, err)
		require.NotZero(t, returned)
		require.True(t, returned.IsSet(api.Event_POST_UPDATE_POD_SANDBOX))
	})

	t.Run("only validation bit with nil Fn should return minimalEventMask", func(t *testing.T) {
		q := &EventQ{}
		var mask stub.EventMask
		mask.Set(api.Event_VALIDATE_CONTAINER_ADJUSTMENT)
		plugin := NewTestPlugin("test", "00", mask, q, &Scope{})
		plugin.ValidateAdjustmentFn = nil

		returned, err := plugin.Configure(context.Background(), "", "runtime", "1.0")
		require.NoError(t, err)
		require.NotZero(t, returned)
		require.True(t, returned.IsSet(api.Event_POST_UPDATE_POD_SANDBOX))
		require.False(t, returned.IsSet(api.Event_VALIDATE_CONTAINER_ADJUSTMENT))
	})

	t.Run("validation bit unset, ValidateAdjustmentFn non-nil should error", func(t *testing.T) {
		// The validator would otherwise be silently never consulted. Setting the
		// bit for the test author is not an option: it would auto-subscribe to an
		// event that changes the runtime's behaviour for every container it
		// creates.
		q := &EventQ{}
		var mask stub.EventMask
		mask.Set(api.Event_RUN_POD_SANDBOX)
		plugin := NewTestPlugin("test", "00", mask, q, &Scope{})
		plugin.ValidateAdjustmentFn = func(context.Context, *api.ValidateContainerAdjustmentRequest) error {
			return nil
		}

		returned, err := plugin.Configure(context.Background(), "", "runtime", "1.0")
		require.Error(t, err)
		require.ErrorContains(t, err, "VALIDATE_CONTAINER_ADJUSTMENT")
		require.Zero(t, returned)
	})

	t.Run("zero mask with a non-nil ValidateAdjustmentFn should error", func(t *testing.T) {
		// A zero mask does not include the validation event either, so this must
		// be reported rather than quietly replaced by minimalEventMask.
		q := &EventQ{}
		plugin := NewTestPlugin("test", "00", 0, q, &Scope{})
		plugin.ValidateAdjustmentFn = func(context.Context, *api.ValidateContainerAdjustmentRequest) error {
			return nil
		}

		returned, err := plugin.Configure(context.Background(), "", "runtime", "1.0")
		require.Error(t, err)
		require.ErrorContains(t, err, "VALIDATE_CONTAINER_ADJUSTMENT")
		require.Zero(t, returned)
	})
}

func TestTestPluginUpdateContainers(t *testing.T) {
	t.Run("should error rather than panic on an unstarted plugin", func(t *testing.T) {
		p := NewTestPlugin("test", "00", 0, &EventQ{}, &Scope{})

		updates, err := p.UpdateContainers([]*api.ContainerUpdate{{ContainerId: "ctr0"}})
		require.ErrorIs(t, err, stub.ErrNoService)
		require.Nil(t, updates)
	})
}

func TestTestPluginStart(t *testing.T) {
	t.Run("should refuse to start the same plugin twice", func(t *testing.T) {
		p := NewTestPlugin("test", "00", 0, &EventQ{}, &Scope{})
		socket := filepath.Join(t.TempDir(), "nri.sock")

		// Nothing is listening, so the first Start fails at the dial. It has
		// still consumed the plugin: readiness is signalled by a channel closed
		// once and never reopened, so a second Start would return before the new
		// Synchronize and make a reconnect test pass vacuously.
		require.Error(t, p.Start(context.Background(), socket, 100*time.Millisecond))

		err := p.Start(context.Background(), socket, 100*time.Millisecond)
		require.Error(t, err)
		require.ErrorContains(t, err, "already been started")
	})
}

func TestTestPluginRecordDisconnect(t *testing.T) {
	t.Run("should record a Disconnected event for the plugin", func(t *testing.T) {
		// recordDisconnect is what Start hands the stub as its onClose
		// notification, so a runtime-initiated disconnect becomes observable.
		q := &EventQ{}
		p := NewTestPlugin("test", "00", 0, q, &Scope{})

		p.recordDisconnect()

		require.True(t, q.Has(&Event{Type: Disconnected, Plugin: "00-test"}))
	})
}

func TestTestPluginFullName(t *testing.T) {
	t.Run("should be the index and the name", func(t *testing.T) {
		p := NewTestPlugin("test", "00", 0, &EventQ{}, &Scope{})
		require.Equal(t, "00-test", p.FullName())
	})
}

func TestTestPluginStop(t *testing.T) {
	t.Run("should be idempotent and record only one Stopped event", func(t *testing.T) {
		// A TestPlugin with stub=nil will pass the idempotency guard but not reach
		// the stub call, so that path does not exercise the stopped flag in practice.
		// Instead, construct one with a non-nil stub so the guard and the stub
		// interaction are both exercised.
		q := &EventQ{}
		p := NewTestPlugin("test", "00", 0, q, &Scope{})

		// Mock stub with Stop and Wait methods
		var stopCalls, waitCalls int
		p.stub = &testStub{
			stopFn: func() { stopCalls++ },
			waitFn: func() { waitCalls++ },
		}

		// First Stop should record exactly one event
		p.Stop()
		events := q.Events()
		require.Len(t, events, 1)
		require.Equal(t, Stopped, events[0].Type)
		require.Equal(t, 1, stopCalls)
		require.Equal(t, 1, waitCalls)

		// Second Stop should be a no-op and record no new event
		p.Stop()
		events = q.Events()
		require.Len(t, events, 1, "second Stop should not add another event")
		require.Equal(t, 1, stopCalls, "stub.Stop should not be called again")
		require.Equal(t, 1, waitCalls, "stub.Wait should not be called again")
	})
}

func TestTestPluginFiltersReturnedUpdates(t *testing.T) {
	newPlugin := func() (*TestPlugin, *EventQ) {
		q := &EventQ{}

		return NewTestPlugin("test", "00", 0, q, &Scope{
			Namespaces: []string{"nri-test"}, RunID: "run0",
		}), q
	}

	t.Run("should pass through an update for a container it has seen", func(t *testing.T) {
		p, q := newPlugin()

		pod := scopedPod("pod0", "nri-test", "run0")
		ctr := testCtr("pod0", "ctr0")

		// Seeing the container in an in-scope callback is what makes it ours.
		_, _, err := p.CreateContainer(context.Background(), pod, ctr)
		require.NoError(t, err)

		u := &api.ContainerUpdate{ContainerId: "ctr0"}
		require.Equal(t, []*api.ContainerUpdate{u}, p.filterUpdates([]*api.ContainerUpdate{u}))
		require.False(t, q.Has(&Event{Type: UpdateDropped}))
	})

	t.Run("should drop an update for a container it has never seen", func(t *testing.T) {
		p, q := newPlugin()

		u := &api.ContainerUpdate{ContainerId: "someone-elses-container"}
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{u}))
		require.True(t, q.Has(&Event{Type: UpdateDropped}),
			"dropping an update must be recorded, not silent")
	})

	t.Run("should drop only the offending entries", func(t *testing.T) {
		p, _ := newPlugin()

		pod := scopedPod("pod0", "nri-test", "run0")
		_, _, err := p.CreateContainer(context.Background(), pod, testCtr("pod0", "ctr0"))
		require.NoError(t, err)

		mine := &api.ContainerUpdate{ContainerId: "ctr0"}
		theirs := &api.ContainerUpdate{ContainerId: "not-mine"}

		require.Equal(t, []*api.ContainerUpdate{mine},
			p.filterUpdates([]*api.ContainerUpdate{mine, theirs}))
	})

	t.Run("should drop everything when it has seen no containers", func(t *testing.T) {
		p, _ := newPlugin()

		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{
			{ContainerId: "ctr0"},
			{ContainerId: "ctr1"},
		}))
	})

	t.Run("should not record a drop for an empty or nil list", func(t *testing.T) {
		p, q := newPlugin()

		require.Empty(t, p.filterUpdates(nil))
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{}))
		require.False(t, q.Has(&Event{Type: UpdateDropped}))
	})

	t.Run("should not note a container it saw out of scope", func(t *testing.T) {
		p, _ := newPlugin()

		_, _, err := p.CreateContainer(context.Background(),
			scopedPod("pod0", "default", "run0"), testCtr("pod0", "ctr0"))
		require.NoError(t, err)

		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "ctr0"}}))
	})
}

// TestTestPluginFiltersEveryExit checks the filter at each of the callback exits
// that can return an update list. A test's returned update reaches the runtime
// verbatim, so an exit left unwrapped is a path to mutating a foreign container.
func TestTestPluginFiltersEveryExit(t *testing.T) {
	var (
		ctx  = context.Background()
		pod  = scopedPod("pod0", "nri-test", "run0")
		ctr  = testCtr("pod0", "ctr0")
		mine = &api.ContainerUpdate{ContainerId: "ctr0", Linux: &api.LinuxContainerUpdate{}}
		// A container the plugin has never been told about: a mistyped id in a
		// test, or a SynchronizeFn mapping over the node-wide list.
		theirs = &api.ContainerUpdate{ContainerId: "not-mine", Linux: &api.LinuxContainerUpdate{}}
		both   = []*api.ContainerUpdate{mine, theirs}
	)

	newPlugin := func() (*TestPlugin, *EventQ) {
		q := &EventQ{}

		return NewTestPlugin("test", "00", 0, q, &Scope{
			Namespaces: []string{"nri-test"}, RunID: "run0",
		}), q
	}

	t.Run("CreateContainer", func(t *testing.T) {
		p, q := newPlugin()
		p.CreateContainerFn = func(context.Context, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
			return nil, both, nil
		}

		_, updates, err := p.CreateContainer(ctx, pod, ctr)
		require.NoError(t, err)
		require.Equal(t, []*api.ContainerUpdate{mine}, updates)
		require.True(t, q.Has(&Event{Type: UpdateDropped, CtrID: "not-mine"}))
	})

	t.Run("UpdateContainer", func(t *testing.T) {
		p, q := newPlugin()
		p.UpdateContainerFn = func(context.Context, *api.PodSandbox, *api.Container, *api.LinuxResources) ([]*api.ContainerUpdate, error) {
			return both, nil
		}

		updates, err := p.UpdateContainer(ctx, pod, ctr, nil)
		require.NoError(t, err)
		require.Equal(t, []*api.ContainerUpdate{mine}, updates)
		require.True(t, q.Has(&Event{Type: UpdateDropped, CtrID: "not-mine"}))
	})

	t.Run("StopContainer", func(t *testing.T) {
		p, q := newPlugin()
		p.StopContainerFn = func(context.Context, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error) {
			return both, nil
		}

		updates, err := p.StopContainer(ctx, pod, ctr)
		require.NoError(t, err)
		require.Equal(t, []*api.ContainerUpdate{mine}, updates)
		require.True(t, q.Has(&Event{Type: UpdateDropped, CtrID: "not-mine"}))
	})

	t.Run("Synchronize", func(t *testing.T) {
		// Synchronize's input is filtered, so a Fn that maps over what it is handed
		// cannot name a foreign container. The output filter still has to hold for
		// an id the Fn names itself — a typo, or an id captured elsewhere.
		p, q := newPlugin()

		var (
			foreignPod = scopedPod("pod1", "default", "run0")
			foreignCtr = testCtr("pod1", "not-mine")
		)

		p.SynchronizeFn = func(_ context.Context, _ []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
			// The dangerous shape: an update for every container it was handed,
			// plus a hardcoded id the filter has never seen.
			updates := make([]*api.ContainerUpdate, 0, len(ctrs)+1)
			for _, c := range ctrs {
				updates = append(updates, &api.ContainerUpdate{ContainerId: c.GetId()})
			}

			return append(updates, theirs), nil
		}

		updates, err := p.Synchronize(ctx,
			[]*api.PodSandbox{pod, foreignPod},
			[]*api.Container{ctr, foreignCtr},
		)
		require.NoError(t, err)
		require.Len(t, updates, 1)
		require.Equal(t, "ctr0", updates[0].GetContainerId())
		require.True(t, q.Has(&Event{Type: UpdateDropped, CtrID: "not-mine"}))
	})

	t.Run("Synchronize should hand SynchronizeFn only in-scope pods and containers", func(t *testing.T) {
		// Input filtering as prevention: the callback never handles foreign data,
		// so it cannot act on it by accident. The orphan container pins the case of
		// a container whose pod is absent from the list altogether.
		p, _ := newPlugin()

		var (
			gotPods []*api.PodSandbox
			gotCtrs []*api.Container
		)

		p.SynchronizeFn = func(_ context.Context, pods []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
			gotPods, gotCtrs = pods, ctrs

			return nil, nil
		}

		_, err := p.Synchronize(ctx,
			[]*api.PodSandbox{pod, scopedPod("pod1", "default", "run0")},
			[]*api.Container{ctr, testCtr("pod1", "not-mine"), testCtr("pod-absent", "orphan")},
		)
		require.NoError(t, err)

		require.Len(t, gotPods, 1)
		require.Equal(t, "pod0", gotPods[0].GetId())
		require.Len(t, gotCtrs, 1)
		require.Equal(t, "ctr0", gotCtrs[0].GetId())
	})

	t.Run("Synchronize should expose the unfiltered protos while filtering the callback", func(t *testing.T) {
		// The assertion that stops the asymmetry being "tidied up". The raw
		// accessors are for fidelity measurement: the pod fields a fidelity test
		// measures are the fields the filter keys on, so measuring them through the
		// filtered callback would be circular — a wrong namespace would look like an
		// absent pod. Exposing the unfiltered view is safe because filterUpdates
		// still guards every exit: a test can look at a foreign workload, not act on
		// one.
		p, _ := newPlugin()

		var (
			foreignPod = scopedPod("pod1", "default", "run0")
			foreignCtr = testCtr("pod1", "not-mine")
			gotPods    []*api.PodSandbox
			gotCtrs    []*api.Container
		)

		p.SynchronizeFn = func(_ context.Context, pods []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
			gotPods, gotCtrs = pods, ctrs

			return nil, nil
		}

		_, err := p.Synchronize(ctx,
			[]*api.PodSandbox{pod, foreignPod},
			[]*api.Container{ctr, foreignCtr},
		)
		require.NoError(t, err)

		// Filtered on the way in to the callback.
		require.Len(t, gotPods, 1)
		require.Len(t, gotCtrs, 1)

		// Unfiltered through the raw accessors, for the same input.
		rawPods := p.RawSyncedPods()
		require.Len(t, rawPods, 2)
		require.Equal(t, []string{"pod0", "pod1"},
			[]string{rawPods[0].GetId(), rawPods[1].GetId()})
		require.Equal(t, "default", rawPods[1].GetNamespace(),
			"a foreign pod must be readable with its fields intact, which is the "+
				"whole point: a wrong namespace must be reportable as a wrong namespace")

		rawCtrs := p.RawSyncedCtrs()
		require.Len(t, rawCtrs, 2)
		require.Equal(t, []string{"ctr0", "not-mine"},
			[]string{rawCtrs[0].GetId(), rawCtrs[1].GetId()})

		// Looking is allowed; acting is not. The output filter still drops an update
		// naming the foreign container the test can now see.
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "not-mine"}}))
	})

	t.Run("Synchronize should clone the protos it retains", func(t *testing.T) {
		// Request-scoped protos must not be retained live: a framework holding the
		// runtime's own messages past the call is a trap.
		p, _ := newPlugin()

		_, err := p.Synchronize(ctx, []*api.PodSandbox{pod}, []*api.Container{ctr})
		require.NoError(t, err)

		rawPods := p.RawSyncedPods()
		require.Len(t, rawPods, 1)
		require.NotSame(t, pod, rawPods[0], "the retained pod must be a clone")
		require.Equal(t, pod.GetNamespace(), rawPods[0].GetNamespace())

		rawCtrs := p.RawSyncedCtrs()
		require.Len(t, rawCtrs, 1)
		require.NotSame(t, ctr, rawCtrs[0], "the retained container must be a clone")
		require.Equal(t, ctr.GetPodSandboxId(), rawCtrs[0].GetPodSandboxId())
	})

	t.Run("Synchronize should still report the unfiltered ids from SyncedPods and SyncedCtrs", func(t *testing.T) {
		// The asymmetry is deliberate: the accessors are diagnostics answering
		// "what did the runtime tell us about", which is itself a conformance
		// question, while what a callback may act on is limited to this run.
		p, _ := newPlugin()

		_, err := p.Synchronize(ctx,
			[]*api.PodSandbox{pod, scopedPod("pod1", "default", "run0")},
			[]*api.Container{ctr, testCtr("pod1", "not-mine"), testCtr("pod-absent", "orphan")},
		)
		require.NoError(t, err)

		require.Equal(t, []string{"pod0", "pod1"}, p.SyncedPods())
		require.Equal(t, []string{"ctr0", "not-mine", "orphan"}, p.SyncedCtrs())
	})

	t.Run("Synchronize should not note a container whose pod it never saw", func(t *testing.T) {
		// An orphan container: no pod in the list has its PodSandboxId, so there is
		// no in-scope pod to inherit from and it must not become updatable.
		p, _ := newPlugin()

		_, err := p.Synchronize(ctx,
			[]*api.PodSandbox{pod},
			[]*api.Container{testCtr("pod-unknown", "orphan")},
		)
		require.NoError(t, err)

		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "orphan"}}))
	})

	t.Run("UpdateContainers", func(t *testing.T) {
		p, q := newPlugin()

		var seen []*api.ContainerUpdate
		p.stub = &testStub{
			updateFn: func(updates []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
				seen = updates

				return nil, nil
			},
		}

		// Note ctr0 the only way there is: an in-scope callback.
		_, _, err := p.CreateContainer(ctx, pod, ctr)
		require.NoError(t, err)

		_, err = p.UpdateContainers(both)
		require.NoError(t, err)
		require.Equal(t, []*api.ContainerUpdate{mine}, seen,
			"the foreign update must be dropped before it reaches the stub")
		require.True(t, q.Has(&Event{Type: UpdateDropped, CtrID: "not-mine"}))
	})

	t.Run("UpdateContainers should keep the nil-stub guard ahead of the filter", func(t *testing.T) {
		p, q := newPlugin()

		updates, err := p.UpdateContainers(both)
		require.ErrorIs(t, err, stub.ErrNoService)
		require.Nil(t, updates)

		// The error and the nil hold whichever order the two run in. That the guard
		// short-circuits is visible only in the queue: a filter that ran first would
		// have recorded a drop for the foreign update.
		require.False(t, q.Has(&Event{Type: UpdateDropped}),
			"the nil-stub guard must short-circuit before the filter runs")
	})

	t.Run("should have a filtered exit for every callback returning updates", func(t *testing.T) {
		// Reflect over TestPlugin's methods so a new callback returning
		// []*api.ContainerUpdate cannot be added without this list, and the
		// reviewer's checklist, being updated alongside it.
		filtered := map[string]bool{
			"CreateContainer":  true,
			"UpdateContainer":  true,
			"StopContainer":    true,
			"Synchronize":      true,
			"UpdateContainers": true,
		}

		updateSlice := reflect.TypeOf([]*api.ContainerUpdate{})

		typ := reflect.TypeOf(&TestPlugin{})
		for i := range typ.NumMethod() {
			m := typ.Method(i)

			var returnsUpdates bool
			for j := range m.Type.NumOut() {
				if m.Type.Out(j) == updateSlice {
					returnsUpdates = true
				}
			}

			if !returnsUpdates {
				continue
			}

			require.True(t, filtered[m.Name],
				"TestPlugin.%s returns []*api.ContainerUpdate; pass it through filterUpdates "+
					"and add it here", m.Name)
		}

		require.Len(t, filtered, 5, "five exits can return an update list")
	})
}

// TestTestPluginNotesTheContainerOfEveryCallback pins the noteContainer call in
// every container callback, not just the three whose return value is filtered.
//
// Without this, removing the call from — say — StartContainer fails no test, and a
// case subscribed only to StartContainer would have its UpdateContainers request
// silently emptied: the update is dropped because the container was never noted,
// and the case does nothing and passes.
func TestTestPluginNotesTheContainerOfEveryCallback(t *testing.T) {
	var (
		rows int
		// Counting rows is only a count of nine callbacks if the rows are distinct.
		// TestTestPluginScopeGuards checks that too, but this test should not depend
		// on a sibling for the meaning of its own assertion.
		seen = map[string]bool{}
	)

	for _, g := range scopeGuards() {
		// Every container callback's event name contains "Container"; no pod
		// callback's does. Deriving the set means a container callback added to the
		// table is covered here automatically.
		if !strings.Contains(string(g.event), "Container") {
			continue
		}

		require.False(t, seen[g.field], "duplicate table row for %s", g.field)
		seen[g.field] = true

		rows++

		t.Run("should note the container "+string(g.event)+" was invoked about", func(t *testing.T) {
			var (
				q     = &EventQ{}
				calls int
				p     = NewTestPlugin("test", "00", 0, q, &Scope{
					Namespaces: []string{"nri-test"}, RunID: "run0",
				})
			)

			g.install(p, &calls)

			// Every container row invokes with testCtr(pod.GetId(), "ctr0").
			_, _ = g.invoke(p, scopedPod("pod0", "nri-test", "run0"))
			require.Equal(t, 1, calls, "the callback must have run")

			require.NotEmpty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "ctr0"}}),
				"%s must note the container it was invoked about, or an update for it "+
					"is dropped and the test that asked for it passes anyway", g.event)
		})
	}

	require.Equal(t, 9, rows, "NRI has 9 container callbacks")
}

// TestTestPluginKeysOnIdsNotNames pins that the allowlist and the Synchronize
// derivation key on ids.
//
// The shared fixtures set Name equal to Id, so recording ctr.GetName() instead of
// ctr.GetId(), or keying the derivation on pod.GetName(), passes every other test
// here — while against a real runtime, where a container id is a hash and its name
// is the Kubernetes container name, it would drop every single update.
func TestTestPluginKeysOnIdsNotNames(t *testing.T) {
	ctx := context.Background()

	newPlugin := func() (*TestPlugin, *EventQ) {
		q := &EventQ{}

		return NewTestPlugin("test", "00", 0, q, &Scope{
			Namespaces: []string{"nri-test"}, RunID: "run0",
		}), q
	}

	t.Run("should note a container by its id and not its name", func(t *testing.T) {
		p, _ := newPlugin()

		// As a runtime reports them: an opaque id, a human name.
		ctr := &api.Container{Id: "c8f3a1deadbeef", PodSandboxId: "pod0", Name: "app"}

		_, _, err := p.CreateContainer(ctx, scopedPod("pod0", "nri-test", "run0"), ctr)
		require.NoError(t, err)

		require.NotEmpty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "c8f3a1deadbeef"}}),
			"an update naming the container's id must be kept")
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "app"}}),
			"the container's name is not an id the runtime would accept")
	})

	t.Run("should derive the Synchronize set from pod ids and not pod names", func(t *testing.T) {
		p, _ := newPlugin()

		pod := scopedPod("p9d4c2cafe", "nri-test", "run0")
		pod.Name = "web"

		var gotCtrs []*api.Container
		p.SynchronizeFn = func(_ context.Context, _ []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
			gotCtrs = ctrs

			return nil, nil
		}

		_, err := p.Synchronize(ctx, []*api.PodSandbox{pod}, []*api.Container{
			{Id: "ours", PodSandboxId: "p9d4c2cafe", Name: "app"},
			// A container claiming the pod's *name* as its sandbox id belongs to
			// nothing we know about.
			{Id: "theirs", PodSandboxId: "web", Name: "app"},
		})
		require.NoError(t, err)

		require.Len(t, gotCtrs, 1)
		require.Equal(t, "ours", gotCtrs[0].GetId())

		require.NotEmpty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "ours"}}))
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "theirs"}}))
	})
}

// TestTestPluginFailsClosedOnEmptyIds pins the two empty-id guards. Both are
// invisible to every other test, and the second is the fail-open direction: an
// empty pod id used as a key would make every container with an empty
// PodSandboxId look in scope.
func TestTestPluginFailsClosedOnEmptyIds(t *testing.T) {
	ctx := context.Background()

	newPlugin := func() (*TestPlugin, *EventQ) {
		q := &EventQ{}

		return NewTestPlugin("test", "00", 0, q, &Scope{
			Namespaces: []string{"nri-test"}, RunID: "run0",
		}), q
	}

	t.Run("should not note a container with no id", func(t *testing.T) {
		p, _ := newPlugin()

		_, _, err := p.CreateContainer(ctx, scopedPod("pod0", "nri-test", "run0"),
			&api.Container{Id: "", PodSandboxId: "pod0", Name: "app"})
		require.NoError(t, err)

		// An update naming no container names whatever the runtime resolves "" to,
		// which is not something this suite may aim at.
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: ""}}))
	})

	t.Run("should not treat an empty pod id as a Synchronize key", func(t *testing.T) {
		p, _ := newPlugin()

		// In scope by namespace, but with no id: keying on it would admit every
		// container whose PodSandboxId is empty, which is the fail-open direction.
		idless := scopedPod("", "nri-test", "run0")

		var gotCtrs []*api.Container
		p.SynchronizeFn = func(_ context.Context, _ []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
			gotCtrs = ctrs

			return nil, nil
		}

		_, err := p.Synchronize(ctx, []*api.PodSandbox{idless}, []*api.Container{
			{Id: "sneaky", PodSandboxId: "", Name: "app"},
		})
		require.NoError(t, err)

		require.Empty(t, gotCtrs,
			"a container with no sandbox id must not inherit an id-less pod's scope")
		require.Empty(t, p.filterUpdates([]*api.ContainerUpdate{{ContainerId: "sneaky"}}))
	})
}

// testStub is a minimal stub mock for testing Stop idempotency and for capturing
// what an UpdateContainers request carries by the time it reaches the stub.
type testStub struct {
	stopFn   func()
	waitFn   func()
	updateFn func([]*api.ContainerUpdate) ([]*api.ContainerUpdate, error)
}

func (s *testStub) Run(context.Context) error   { return nil }
func (s *testStub) Start(context.Context) error { return nil }

func (s *testStub) Stop() {
	if s.stopFn != nil {
		s.stopFn()
	}
}

func (s *testStub) Wait() {
	if s.waitFn != nil {
		s.waitFn()
	}
}

func (s *testStub) UpdateContainers(updates []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	if s.updateFn != nil {
		return s.updateFn(updates)
	}

	return nil, nil
}

func (s *testStub) RegistrationTimeout() time.Duration { return 0 }
func (s *testStub) RequestTimeout() time.Duration      { return 0 }
func (s *testStub) Logger() nrilog.Logger              { return nil }
func (s *testStub) RuntimeNRIVersion() string          { return "" }
func (s *testStub) PluginNRIVersion() string           { return "" }

var _ stub.Stub = &testStub{}
