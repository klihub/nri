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
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// staleDriver is a Driver that also reports stale pods, which is the optional
// surface reportStalePods looks for.
type staleDriver struct {
	fakeDriver

	stale []StalePod
}

func (d *staleDriver) StalePods() []StalePod { return d.stale }

var _ stalePodLister = &staleDriver{}

func TestStaleReport(t *testing.T) {
	stale := []StalePod{{ID: "abc123", Name: "nri-test-0-pod0", RunID: "1a2b3c4d"}}

	t.Run("should say nothing when no pod is stale", func(t *testing.T) {
		// The overwhelmingly common case is a clean node. A notice that prints an
		// empty list on every run is noise, and noise is what gets filtered out.
		require.Empty(t, staleReport("nri-test", nil))
		require.Empty(t, staleReport("nri-test", []StalePod{}))
	})

	t.Run("should name the pod, its name and its run id", func(t *testing.T) {
		// All three, because all three are what an operator needs: the id to remove
		// it by hand, the name to recognise it, the run id to tell which run to wait
		// for.
		out := staleReport("nri-test", stale)

		require.Contains(t, out, "abc123")
		require.Contains(t, out, "nri-test-0-pod0")
		require.Contains(t, out, "1a2b3c4d")
		require.Contains(t, out, "nri-test", "the namespace the pods were found in")
	})

	t.Run("should say why the suite leaves them alone and what to do", func(t *testing.T) {
		out := staleReport("nri-test", stale)

		require.Contains(t, out, "will not remove")
		require.Contains(t, out, "crashed")
		require.Contains(t, out, "by hand")
	})

	t.Run("should prefix every line as a warning", func(t *testing.T) {
		// So the notice reads as one block in test output, next to the other
		// WARNING lines Setup and Teardown print.
		out := staleReport("nri-test", append(stale, StalePod{ID: "def456", RunID: "9z"}))

		for _, line := range bytes.Split([]byte(out), []byte("\n")) {
			if len(line) == 0 {
				continue
			}

			require.True(t, bytes.HasPrefix(line, []byte("WARNING: ")),
				"every line must be a WARNING, got %q", string(line))
		}
	})

	t.Run("should cope with a pod the listing gave no name", func(t *testing.T) {
		out := staleReport("nri-test", []StalePod{{ID: "def456", RunID: "9z"}})

		require.Contains(t, out, "def456")
		require.NotContains(t, out, "()", "an absent name must not print as empty parens")
	})

	t.Run("should count the pods it names", func(t *testing.T) {
		one := staleReport("nri-test", stale)
		two := staleReport("nri-test", append(stale, StalePod{ID: "def456", RunID: "9z"}))

		require.Contains(t, one, "1 pod ")
		require.Contains(t, two, "2 pods ")
	})
}

func TestReportStalePods(t *testing.T) {
	stale := []StalePod{{ID: "abc123", Name: "pod0", RunID: "1a2b3c4d"}}

	t.Run("should print the notice for a driver that reports stale pods", func(t *testing.T) {
		var out bytes.Buffer

		reportStalePods(&out, &staleDriver{stale: stale}, "nri-test")
		require.Contains(t, out.String(), "abc123")
	})

	t.Run("should print nothing when the driver reports none", func(t *testing.T) {
		var out bytes.Buffer

		reportStalePods(&out, &staleDriver{}, "nri-test")
		require.Empty(t, out.String())
	})

	t.Run("should print nothing for a driver without the surface", func(t *testing.T) {
		// The notice is a diagnostic, so a driver that cannot enumerate other runs'
		// pods stays a legal driver rather than being forced to stub the method.
		var out bytes.Buffer

		reportStalePods(&out, &fakeDriver{}, "nri-test")
		require.Empty(t, out.String())
	})

	t.Run("should print nothing for a nil driver", func(t *testing.T) {
		// Setup reports after its own sweep, but a caller that has not built a driver
		// must not panic here.
		var out bytes.Buffer

		reportStalePods(&out, nil, "nri-test")
		require.Empty(t, out.String())
	})
}

func TestStalePodsDoNotChangeTheRunsOutcome(t *testing.T) {
	// The notice is information, not a fault: a stale peer's pod must not fail a
	// case, must not fail cleanup, and must not reach the leak guard's verdict. The
	// guard's own listing is what decides that verdict, and a stale pod is absent
	// from it by construction -- Scope.Matches excludes another run's id -- which the
	// driver's own tests pin. Here we assert the suite-level consequence.
	ft := &fakeT{}
	d := &staleDriver{stale: []StalePod{{ID: "abc123", Name: "pod0", RunID: "other"}}}

	useDriver(t, d)
	useConfig(t, &Config{Namespace: "nri-test", RunID: "run0", Timeout: time.Second})

	s := newFakeSuite(ft, &Pod{ID: "p0"})
	s.cleanup()

	require.Empty(t, ft.errors, "a stale peer's pod must not fail the case")
	require.Equal(t, []string{"p0"}, d.removed, "cleanup still removes the case's own pods")

	// And the pod is still named where the operator will see it, so "does not fail
	// the run" does not quietly mean "is not mentioned".
	require.Contains(t, staleReport("nri-test", d.StalePods()), "abc123")
}
