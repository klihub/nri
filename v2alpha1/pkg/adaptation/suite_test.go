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

package runtime_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	nri "github.com/containerd/nri/v2alpha1/pkg/adaptation"
	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/sirupsen/logrus"

	"github.com/containerd/nri/v2alpha1/pkg/stub"

	"github.com/pkg/errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRuntime(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NRI Runtime")
}

const (
	startupTimeout = 2 * time.Second
)

// A test suite consist of a runtime and a set of plugins.
type Suite struct {
	dir     string        //  directory to create for test
	runtime *mockRuntime  // runtime instance for test
	plugins []*mockPlugin // plugin intances for test
}

// SuiteOption can be applied to a suite.
type SuiteOption func(s *Suite) error

// Prepare test suite, creating test directory and configuration.
func (s *Suite) Prepare(config string, runtime *mockRuntime, plugins ...*mockPlugin) string {
	var (
		dir string
		etc string
		cfg string
	)

	logrus.SetLevel(logrus.ErrorLevel)

	dir = GinkgoT().TempDir()
	etc = filepath.Join(dir, "etc", "nri")
	cfg = filepath.Join(etc, "nri.conf")

	Expect(os.MkdirAll(etc, 0o755)).To(Succeed())
	Expect(ioutil.WriteFile(cfg, []byte(config), 0o644)).To(Succeed())

	s.dir = dir
	s.runtime = runtime
	s.plugins = plugins

	return dir
}

// Startup starts up the test suite.
func (s *Suite) Startup() {
	s.StartRuntime()
	s.StartPlugins()
	s.WaitForPluginsToSync()
}

// StartRuntime starts the suite runtime.
func (s *Suite) StartRuntime() {
	Expect(s.runtime.Start(s.dir)).To(Succeed())
}

// StartPlugins starts the suite plugins.
func (s *Suite) StartPlugins() {
	for _, plugin := range s.plugins {
		Expect(plugin.Start(s.dir)).To(Succeed())
	}
}

// WaitForPluginsToSync waits for the suite plugins to get synchronized.
func (s *Suite) WaitForPluginsToSync() {
	timeout := time.After(startupTimeout)
	for _, plugin := range s.plugins {
		Expect(plugin.Wait(PluginSynchronized, timeout)).To(Succeed())
	}
}

// Cleanup the test suite.
func (s *Suite) Cleanup() {
	s.runtime.Stop()
	// XXX FIXME:
	//   This panics...
	//for _, plugin := range s.plugins {
	//    plugin.Stop()
	//}
	Expect(os.RemoveAll(s.dir)).To(Succeed())
}

// ------------------------------------

func Log(format string, args ...interface{}) {
	GinkgoWriter.Printf(format+"\n", args...)
}

type mockRuntime struct {
	runtime *nri.Adaptation
	pods    map[string]*api.PodSandbox
	ctrs    map[string]*api.Container

	updateFn nri.UpdateFn
}

func (m *mockRuntime) Start(dir string) error {
	var err error

	if m.runtime != nil {
		return errors.New("mock runtime already started")
	}

	m.runtime, err = nri.New("mockRuntime", "0.0.1", m.synchronize, m.update,
		nri.WithConfigPath(filepath.Join(dir, "etc", "nri", "nri.conf")),
		nri.WithPluginPath(filepath.Join(dir, "opt", "nri", "plugins")),
		nri.WithSocketPath(filepath.Join(dir, "nri.sock")),
	)
	if err != nil {
		return err
	}

	if m.pods == nil {
		m.pods = make(map[string]*api.PodSandbox)
	}
	if m.ctrs == nil {
		m.ctrs = make(map[string]*api.Container)
	}

	if m.updateFn == nil {
		m.updateFn = func(context.Context, []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
			return nil, nil
		}
	}

	return m.runtime.Start()
}

func (m *mockRuntime) Stop() {
	if m.runtime != nil {
		m.runtime.Stop()
		m.runtime = nil
	}
}

func (m *mockRuntime) synchronize(ctx context.Context, cb nri.SyncCB) error {
	var (
		pods []*api.PodSandbox
		ctrs []*api.Container
		ids  []string
	)

	for id := range m.pods {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		pods = append(pods, m.pods[id])
	}

	ids = nil
	for id := range m.ctrs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ctrs = append(ctrs, m.ctrs[id])
	}

	_, err := cb(ctx, pods, ctrs)
	return err
}

func (m *mockRuntime) startStopPodAndContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	err := m.runtime.RunPodSandbox(ctx, &api.StateChangeEvent{
		Pod: pod,
	})
	if err != nil {
		return err
	}

	_, err = m.runtime.CreateContainer(ctx, &api.CreateContainerRequest{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	err = m.runtime.PostCreateContainer(ctx, &api.StateChangeEvent{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	err = m.runtime.StartContainer(ctx, &api.StateChangeEvent{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	err = m.runtime.PostStartContainer(ctx, &api.StateChangeEvent{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	_, err = m.runtime.UpdateContainer(ctx, &api.UpdateContainerRequest{
		Pod:            pod,
		Container:      ctr,
		LinuxResources: &api.LinuxResources{},
	})
	if err != nil {
		return err
	}

	err = m.runtime.PostUpdateContainer(ctx, &api.StateChangeEvent{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	_, err = m.runtime.StopContainer(ctx, &api.StopContainerRequest{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	err = m.runtime.RemoveContainer(ctx, &api.StateChangeEvent{
		Pod:       pod,
		Container: ctr,
	})
	if err != nil {
		return err
	}

	err = m.runtime.StopPodSandbox(ctx, &api.StateChangeEvent{
		Pod: pod,
	})
	if err != nil {
		return err
	}

	err = m.runtime.RemovePodSandbox(ctx, &api.StateChangeEvent{
		Pod: pod,
	})
	if err != nil {
		return err
	}

	return nil
}

func (m *mockRuntime) update(ctx context.Context, updates []*nri.ContainerUpdate) ([]*nri.ContainerUpdate, error) {
	return m.updateFn(ctx, updates)
}

type mockPlugin struct {
	name string
	idx  string
	stub stub.Stub
	mask stub.EventMask

	q    *EventQ
	pods map[string]*api.PodSandbox
	ctrs map[string]*api.Container

	runPodSandbox       func(*mockPlugin, *api.PodSandbox, *api.Container)
	stopPodSandbox      func(*mockPlugin, *api.PodSandbox, *api.Container)
	removePodSandbox    func(*mockPlugin, *api.PodSandbox, *api.Container)
	createContainer     func(*mockPlugin, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
	postCreateContainer func(*mockPlugin, *api.PodSandbox, *api.Container)
	startContainer      func(*mockPlugin, *api.PodSandbox, *api.Container)
	postStartContainer  func(*mockPlugin, *api.PodSandbox, *api.Container)
	updateContainer     func(*mockPlugin, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	postUpdateContainer func(*mockPlugin, *api.PodSandbox, *api.Container)
	stopContainer       func(*mockPlugin, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	removeContainer     func(*mockPlugin, *api.PodSandbox, *api.Container)
}

var (
	_ = stub.ConfigureInterface(&mockPlugin{})
	_ = stub.SynchronizeInterface(&mockPlugin{})
	_ = stub.RunPodInterface(&mockPlugin{})
	_ = stub.StopPodInterface(&mockPlugin{})
	_ = stub.RemovePodInterface(&mockPlugin{})
	_ = stub.CreateContainerInterface(&mockPlugin{})
	_ = stub.StartContainerInterface(&mockPlugin{})
	_ = stub.UpdateContainerInterface(&mockPlugin{})
	_ = stub.StopContainerInterface(&mockPlugin{})
	_ = stub.RemoveContainerInterface(&mockPlugin{})
	_ = stub.PostCreateContainerInterface(&mockPlugin{})
	_ = stub.PostStartContainerInterface(&mockPlugin{})
	_ = stub.PostUpdateContainerInterface(&mockPlugin{})
)

func (m *mockPlugin) Log(format string, args ...interface{}) {
	Log("* [plugin %s-%s] "+format, append([]interface{}{m.idx, m.name}, args...)...)
}

func (m *mockPlugin) SetFallbackName(name string, idx int) {
	if m.name == "" {
		m.name = name
	}
	if m.idx == "" {
		m.idx = fmt.Sprintf("%02d", idx)
	}
}

func (m *mockPlugin) Wait(e *Event, deadline <-chan time.Time) error {
	_, err := m.q.Wait(e, deadline)
	return err
}

func (m *mockPlugin) Events() []*Event {
	return m.q.Events()
}

func (m *mockPlugin) EventQ() *EventQ {
	return m.q
}

func (m *mockPlugin) Start(dir string) error {
	var err error

	if m.stub != nil {
		return errors.Errorf("plugin %s-%s already started", m.idx, m.name)
	}

	if m.name == "" {
		m.name = "mock-plugin"
	}
	if m.idx == "" {
		m.idx = "00"
	}
	if m.mask == 0 {
		m.mask = api.ValidEvents
	}

	m.q = &EventQ{}

	m.Log("Start()...")

	m.stub, err = stub.New(m,
		stub.WithPluginName(m.name),
		stub.WithPluginIdx(m.idx),
		stub.WithSocketPath(filepath.Join(dir, "nri.sock")),
		stub.WithOnClose(m.onClose),
	)
	if err != nil {
		m.q.Add(PluginStartupError)
		return err
	}

	m.pods = make(map[string]*api.PodSandbox)
	m.ctrs = make(map[string]*api.Container)

	if m.runPodSandbox == nil {
		m.runPodSandbox = nopEvent
	}
	if m.stopPodSandbox == nil {
		m.stopPodSandbox = nopEvent
	}
	if m.removePodSandbox == nil {
		m.removePodSandbox = nopEvent
	}
	if m.createContainer == nil {
		m.createContainer = nopCreateContainer
	}
	if m.postCreateContainer == nil {
		m.postCreateContainer = nopEvent
	}
	if m.startContainer == nil {
		m.startContainer = nopEvent
	}
	if m.postStartContainer == nil {
		m.postStartContainer = nopEvent
	}
	if m.updateContainer == nil {
		m.updateContainer = nopUpdateContainer
	}
	if m.postUpdateContainer == nil {
		m.postUpdateContainer = nopEvent
	}
	if m.stopContainer == nil {
		m.stopContainer = nopStopContainer
	}
	if m.removeContainer == nil {
		m.removeContainer = nopEvent
	}

	err = m.stub.Start(context.Background())
	if err != nil {
		m.q.Add(PluginStartupError)
		return err
	}

	return nil
}

func (m *mockPlugin) Stop() {
	m.stub.Stop()
	m.stub.Wait()

	m.q.Add(PluginStopped)
}

func (m *mockPlugin) onClose() {
	m.stub.Stop()
	m.stub.Wait()

	m.q.Add(PluginDisconnected)
}

func (m *mockPlugin) Configure(cfg, runtime, version string) (stub.EventMask, error) {
	m.q.Add(PluginConfigured)

	return m.mask, nil
}

func (m *mockPlugin) Synchronize(pods []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
	for _, pod := range pods {
		m.pods[pod.Id] = pod
	}
	for _, ctr := range ctrs {
		m.ctrs[ctr.Id] = ctr
	}

	m.q.Add(PluginSynchronized)

	return nil, nil
}

func (m *mockPlugin) Shutdown() {
	m.q.Add(PluginShutdown)
}

func (m *mockPlugin) RunPodSandbox(pod *api.PodSandbox) {
	m.pods[pod.Id] = pod
	m.runPodSandbox(m, pod, nil)
	m.q.Add(PodSandboxEvent(pod, RunPodSandbox))
}

func (m *mockPlugin) StopPodSandbox(pod *api.PodSandbox) {
	m.pods[pod.Id] = pod
	m.stopPodSandbox(m, pod, nil)
	m.q.Add(PodSandboxEvent(pod, StopPodSandbox))
}

func (m *mockPlugin) RemovePodSandbox(pod *api.PodSandbox) {
	delete(m.pods, pod.Id)
	m.removePodSandbox(m, pod, nil)
	m.q.Add(PodSandboxEvent(pod, RemovePodSandbox))
}

func (m *mockPlugin) CreateContainer(pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, CreateContainer))

	return m.createContainer(m, pod, ctr)
}

func (m *mockPlugin) PostCreateContainer(pod *api.PodSandbox, ctr *api.Container) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, PostCreateContainer))

	m.postCreateContainer(m, pod, ctr)
}

func (m *mockPlugin) StartContainer(pod *api.PodSandbox, ctr *api.Container) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, StartContainer))

	m.startContainer(m, pod, ctr)
}

func (m *mockPlugin) PostStartContainer(pod *api.PodSandbox, ctr *api.Container) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, PostStartContainer))

	m.postStartContainer(m, pod, ctr)
}

func (m *mockPlugin) UpdateContainer(pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, UpdateContainer))

	return m.updateContainer(m, pod, ctr)
}

func (m *mockPlugin) PostUpdateContainer(pod *api.PodSandbox, ctr *api.Container) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, PostUpdateContainer))

	m.postUpdateContainer(m, pod, ctr)
}

func (m *mockPlugin) StopContainer(pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	m.pods[pod.Id] = pod
	m.ctrs[ctr.Id] = ctr
	m.q.Add(ContainerEvent(ctr, StopContainer))

	return m.stopContainer(m, pod, ctr)
}

func (m *mockPlugin) RemoveContainer(pod *api.PodSandbox, ctr *api.Container) {
	delete(m.ctrs, ctr.Id)
	m.q.Add(ContainerEvent(ctr, RemoveContainer))

	m.removeContainer(m, pod, ctr)
}

func nopEvent(*mockPlugin, *api.PodSandbox, *api.Container) {
}

func nopCreateContainer(*mockPlugin, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	return nil, nil, nil
}

func nopUpdateContainer(*mockPlugin, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error) {
	return nil, nil
}

func nopStopContainer(*mockPlugin, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error) {
	return nil, nil
}

type EventType string

const (
	Started      = "started"
	Configured   = "configured"
	Synchronized = "synchronized"
	StartupError = "startup-error"
	Shutdown     = "shutdown"
	Disconnected = "closed"
	Stopped      = "stopped"

	RunPodSandbox       = "RunPodSandbox"
	StopPodSandbox      = "StopPodSandbox"
	RemovePodSandbox    = "RemovePodSandbox"
	CreateContainer     = "CreateContainer"
	StartContainer      = "StartContainer"
	UpdateContainer     = "UpdateContainer"
	StopContainer       = "StopContainer"
	RemoveContainer     = "RemoveContainer"
	PostCreateContainer = "PostCreateContainer"
	PostStartContainer  = "PostStartContainer"
	PostUpdateContainer = "PostUpdateContainer"

	Error   = "Error"
	Timeout = ""
)

type Event struct {
	Type EventType
	Pod  *api.PodSandbox
	Ctr  *api.Container
}

var (
	PluginConfigured   = &Event{Type: Configured}
	PluginSynchronized = &Event{Type: Synchronized}
	PluginStartupError = &Event{Type: StartupError}
	PluginShutdown     = &Event{Type: Shutdown}
	PluginDisconnected = &Event{Type: Disconnected}
	PluginStopped      = &Event{Type: Stopped}

	PodSandboxEvent = func(pod *api.PodSandbox, t EventType) *Event {
		return &Event{Type: t, Pod: pod}
	}
	ContainerEvent = func(ctr *api.Container, t EventType) *Event {
		return &Event{Type: t, Ctr: ctr}
	}
)

func (e *Event) Matches(o *Event) bool {
	if e.Type != o.Type {
		return false
	}
	if e.Pod != nil && o.Pod != nil {
		if e.Pod.Id != o.Pod.Id {
			return false
		}
	}
	if e.Ctr != nil && o.Ctr != nil {
		if e.Ctr.Id != o.Ctr.Id || e.Ctr.PodSandboxId != o.Ctr.PodSandboxId {
			return false
		}
	}
	return true
}

func (e *Event) String() string {
	str := ""
	switch {
	case e.Ctr != nil:
		str += e.Ctr.PodSandboxId + ":" + e.Ctr.Id + "/"
	case e.Pod != nil:
		str += e.Pod.Id + "/"
	}
	return str + string(e.Type)
}

type EventQ struct {
	sync.Mutex
	q []*Event
	c chan *Event
}

func (q *EventQ) Add(e *Event) {
	q.Lock()
	defer q.Unlock()
	q.q = append(q.q, e)
	if q.c != nil {
		q.c <- e
	}
}

func (q *EventQ) Reset(e *Event) {
	q.Lock()
	defer q.Unlock()
	q.q = []*Event{}
}

func (q *EventQ) Events() []*Event {
	q.Lock()
	defer q.Unlock()
	var events []*Event
	events = append(events, q.q...)
	return events
}

func (q *EventQ) Has(e *Event) bool {
	q.Lock()
	defer q.Unlock()
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

func (q *EventQ) Wait(w *Event, deadline <-chan time.Time) (*Event, error) {
	var unlocked bool
	q.Lock()
	defer func() {
		if !unlocked {
			q.Unlock()
		}
	}()

	if e := q.search(w); e != nil {
		return e, nil
	}

	if q.c != nil {
		return nil, errors.New("event queue already busy Wait()ing")
	}
	q.c = make(chan *Event, 16)
	defer func() {
		c := q.c
		q.c = nil
		close(c)
	}()

	q.Unlock()
	unlocked = true
	for {
		select {
		case e := <-q.c:
			if e.Matches(w) {
				return e, nil
			}
		case <-deadline:
			return nil, errors.Errorf("event queue timed out Wait()ing for %s...", w)
		}
	}
}
