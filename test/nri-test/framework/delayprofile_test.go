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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeDelayFile puts body in a file of its own and returns the path.
func writeDelayFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "delays.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestDelayFileAppliesAProfileToTheNamedPlugin(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  slow-policy:
    CreateContainer: 50ms
    StopContainer: 20ms
plugins:
  1: slow-policy
`)

	p, err := NewScalePlan(3, nil, []DelaySource{{Path: path}})
	require.NoError(t, err)

	require.Equal(t, 50*time.Millisecond, p.Delay(1, CreateContainer))
	require.Equal(t, 20*time.Millisecond, p.Delay(1, StopContainer))
	require.Zero(t, p.Delay(1, RunPodSandbox), "an event the profile omits is not delayed")
	require.Zero(t, p.Delay(2, CreateContainer), "a plugin the mapping omits gets nothing")
	require.Zero(t, p.Delay(0, CreateContainer), "the recorder is never delayed")
}

// "*" means the plugins the mapping did not name, so an explicit entry wins by
// being explicit and nothing depends on YAML map ordering, which has none.
func TestDelayFileStarCoversTheRestOfTheSimulatedPlugins(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  slow:
    CreateContainer: 50ms
  quick:
    CreateContainer: 1ms
    StopContainer: 1ms
plugins:
  2: slow
  "*": quick
`)

	p, err := NewScalePlan(4, nil, []DelaySource{{Path: path}})
	require.NoError(t, err)

	require.Equal(t, time.Millisecond, p.Delay(1, CreateContainer))
	require.Equal(t, 50*time.Millisecond, p.Delay(2, CreateContainer), "the named plugin keeps its own profile")
	require.Equal(t, time.Millisecond, p.Delay(3, CreateContainer))
	require.Zero(t, p.Delay(0, CreateContainer), "the recorder is not one of the rest")

	// The named plugin is not covered by "*" at all, rather than covered and
	// then overridden: an event only the "*" profile names must not reach it.
	// Without this, applying "*" first and letting the named entry win would
	// pass, and the two differ exactly here.
	require.Zero(t, p.Delay(2, StopContainer))
	require.Equal(t, time.Millisecond, p.Delay(1, StopContainer))
}

// With no recorder there is no plugin for "*" to skip.
func TestDelayFileStarCoversPluginZeroWhenTraced(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  quick:
    CreateContainer: 1ms
plugins:
  "*": quick
`)

	p, err := NewTracedScalePlan(2, nil, []DelaySource{{Path: path}})
	require.NoError(t, err)

	require.Equal(t, time.Millisecond, p.Delay(0, CreateContainer))
	require.Equal(t, time.Millisecond, p.Delay(1, CreateContainer))
}

// Sources are merged per event, not swapped wholesale: a later one overrides
// only the pairs it names.
func TestDelayFileLaterSourceOverridesPerEvent(t *testing.T) {
	base := writeDelayFile(t, `
profiles:
  base:
    CreateContainer: 50ms
    StopContainer: 50ms
plugins:
  1: base
`)
	refine := writeDelayFile(t, `
profiles:
  refined:
    CreateContainer: 5ms
plugins:
  1: refined
`)

	p, err := NewScalePlan(2, nil, []DelaySource{{Path: base}, {Path: refine}})
	require.NoError(t, err)

	require.Equal(t, 5*time.Millisecond, p.Delay(1, CreateContainer), "the later file wins for the event it names")
	require.Equal(t, 50*time.Millisecond, p.Delay(1, StopContainer), "and leaves the rest of the profile standing")
}

// The two flags compose in command-line order, which is the whole reason they
// share one list.
func TestDelayFileAndInlineComposeInOrder(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  base:
    CreateContainer: 50ms
    StopContainer: 50ms
plugins:
  1: base
`)

	p, err := NewScalePlan(2, nil, []DelaySource{
		{Path: path},
		{Spec: "1/CreateContainer=5ms"},
	})
	require.NoError(t, err)
	require.Equal(t, 5*time.Millisecond, p.Delay(1, CreateContainer), "the inline item came later")
	require.Equal(t, 50*time.Millisecond, p.Delay(1, StopContainer))

	p, err = NewScalePlan(2, nil, []DelaySource{
		{Spec: "1/CreateContainer=5ms"},
		{Path: path},
	})
	require.NoError(t, err)
	require.Equal(t, 50*time.Millisecond, p.Delay(1, CreateContainer), "the file came later")
}

// Clearing one pair needs no new syntax: a zero wins like any other value.
func TestDelayFileZeroClearsAnEarlierDelay(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  none:
    CreateContainer: 0s
plugins:
  1: none
`)

	p, err := NewScalePlan(2, nil, []DelaySource{{Spec: "50ms"}, {Path: path}})
	require.NoError(t, err)

	require.Zero(t, p.Delay(1, CreateContainer))
	require.Equal(t, 50*time.Millisecond, p.Delay(1, StopContainer))
}

// A run has to be able to say which scenario it simulated, and the plot keys
// off the name alone.
func TestDelayFileDescribeNamesTheProfile(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  slow-policy:
    CreateContainer: 50ms
plugins:
  1: slow-policy
`)

	p, err := NewScalePlan(2, nil, []DelaySource{{Path: path}})
	require.NoError(t, err)

	d := p.Describe()

	require.Contains(t, d[1], "slow-policy")
	require.Contains(t, d[1], "CreateContainer=50ms")
	require.NotContains(t, d[0], "slow-policy", "the recorder has no profile")
}

func TestDelayFileRejectsBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		plugins int
		body    string
		wants   string
	}{
		"unknown event": {plugins: 2, body: `
profiles:
  p:
    Bogus: 1s
plugins:
  1: p
`, wants: "Bogus"},
		"bad duration": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: soon
plugins:
  1: p
`, wants: "soon"},
		"negative duration": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: -1s
plugins:
  1: p
`, wants: "negative"},
		"undefined profile": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
plugins:
  1: typo
`, wants: "typo"},
		"index too high": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
plugins:
  7: p
`, wants: "7"},
		"delays the recorder": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
plugins:
  0: p
`, wants: "recorder"},
		// A section name that is almost right: the message has to name it,
		// whichever check catches it.
		"misspelled section": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
plugin:
  1: p
`, wants: "plugin"},
		// Only a strict decode can catch this one. The case above cannot
		// stand in for it: a non-strict decode leaves the mapping empty, so
		// the missing-mapping check fires and the file is still refused. Here
		// the mapping is valid, so nothing else has anything to object to.
		"stray key beside a valid mapping": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
plugins:
  1: p
delay-profiles:
  p: 1s
`, wants: "delay-profiles"},
		"no mapping at all": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
`, wants: "plugins"},
		"star with no simulated plugin": {plugins: 1, body: `
profiles:
  p:
    CreateContainer: 1s
plugins:
  "*": p
`, wants: "no simulated plugins"},
		"not a plugin key": {plugins: 2, body: `
profiles:
  p:
    CreateContainer: 1s
plugins:
  first: p
`, wants: "first"},
		"not yaml": {plugins: 2, body: "profiles: [", wants: ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewScalePlan(tc.plugins, nil, []DelaySource{{Path: writeDelayFile(t, tc.body)}})

			require.Error(t, err)
			if tc.wants != "" {
				require.ErrorContains(t, err, tc.wants)
			}
		})
	}
}

func TestDelayFileRejectsAMissingFile(t *testing.T) {
	_, err := NewScalePlan(2, nil, []DelaySource{{Path: filepath.Join(t.TempDir(), "absent.yaml")}})

	require.Error(t, err)
	require.ErrorContains(t, err, "absent.yaml")
}

// One file can hold a library of profiles: an unmapped one is not a mistake.
func TestDelayFileAcceptsAProfileNothingUses(t *testing.T) {
	path := writeDelayFile(t, `
profiles:
  used:
    CreateContainer: 5ms
  spare:
    StopContainer: 1s
plugins:
  1: used
`)

	p, err := NewScalePlan(2, nil, []DelaySource{{Path: path}})
	require.NoError(t, err)

	require.Equal(t, 5*time.Millisecond, p.Delay(1, CreateContainer))
	require.Zero(t, p.Delay(1, StopContainer))
}
