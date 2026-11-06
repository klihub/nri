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

package suite

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

func TestPluginConnection(t *testing.T) {
	t.Run("should accept a plugin with a valid name", func(t *testing.T) {
		s := framework.NewSuite(t, "conn-valid-name")

		plugin := s.NewPlugin("abcd-0123+EFGH_4567.ijkl", "05", 0)
		s.StartPlugins(plugin)

		require.True(t, s.Q().Has(&framework.Event{
			Type:   framework.Synchronized,
			Plugin: plugin.FullName(),
		}))
	})

	t.Run("should reject a plugin with an invalid name", func(t *testing.T) {
		s := framework.NewSuite(t, "conn-invalid-name")

		// Positive control: a valid name must connect from this same process, at this
		// same socket, at this same moment. Without it, this subtest would pass on any
		// startup failure at all -- a missing socket, a timeout, or the permission
		// denial an unprivileged run produces -- and would prove nothing about name
		// validation.
		valid := s.NewPlugin("valid-control", "10", 0)
		s.StartPlugins(valid)

		// The positive control above rules out the socket, permission and timeout
		// family of failures; asserting on the message closes the rest, since a bare
		// require.Error passes for a rejection for any reason at all.
		//
		// This couples to pkg/adaptation's wording ("invalid plugin name: %w" in
		// plugin.go), which is acceptable precisely because pkg/adaptation is the
		// code under conformance test: both containerd and CRI-O vendor that exact
		// code, and the text survives the ttrpc round trip.
		invalid := s.NewPlugin("foo,bar", "11", 0)
		err := s.StartPluginExpectingError(invalid)
		require.Error(t, err,
			"a plugin name containing a comma must be rejected")
		require.ErrorContains(t, err, "invalid plugin name",
			"the rejection must be about the plugin's name, not about its connection")

		require.False(t, s.Q().Has(&framework.Event{
			Type:   framework.Synchronized,
			Plugin: invalid.FullName(),
		}), "a plugin rejected for an invalid name must never synchronize")
	})

	t.Run("should configure and then synchronize a plugin", func(t *testing.T) {
		s := framework.NewSuite(t, "conn-configure")

		plugin := s.NewPlugin("configure", "00", 0)
		s.StartPlugins(plugin)

		events := s.Q().EventsFor(plugin.FullName())
		require.Len(t, events, 2, "expected exactly Configured and Synchronized")
		require.Equal(t, framework.Configured, events[0].Type)
		require.Equal(t, framework.Synchronized, events[1].Type)
	})

	t.Run("should allow several plugins to connect at once", func(t *testing.T) {
		s := framework.NewSuite(t, "conn-multiple")

		var (
			p0 = s.NewPlugin("first", "00", 0)
			p1 = s.NewPlugin("second", "50", 0)
			p2 = s.NewPlugin("third", "99", 0)
		)
		s.StartPlugins(p0, p1, p2)

		for _, p := range []*framework.TestPlugin{p0, p1, p2} {
			require.True(t, s.Q().Has(&framework.Event{
				Type:   framework.Synchronized,
				Plugin: p.FullName(),
			}), "plugin %s did not synchronize", p.FullName())
		}
	})
}

func TestPluginConfigurationRequest(t *testing.T) {
	t.Run("should pass runtime name and version to the plugin", func(t *testing.T) {
		s := framework.NewSuite(t, "config-runtime-version")

		plugin := s.NewPlugin("config", "00", 0)
		s.StartPlugins(plugin)

		require.NotEmpty(t, plugin.RuntimeName(),
			"the runtime must tell plugins its name")
		require.NotEmpty(t, plugin.RuntimeVersion(),
			"the runtime must tell plugins its version")

		// The same runtime the CRI client talks to must be the one behind NRI.
		require.Equal(t, framework.RuntimeName(), plugin.RuntimeName())
		require.Equal(t, framework.RuntimeVersion(), plugin.RuntimeVersion())
	})

	t.Run("should pass usable timeouts to the plugin", func(t *testing.T) {
		s := framework.NewSuite(t, "config-timeouts")

		plugin := s.NewPlugin("timeouts", "00", 0)
		s.StartPlugins(plugin)

		require.Positive(t, plugin.RegistrationTimeout(),
			"the runtime must advertise a registration timeout")
		require.Positive(t, plugin.RequestTimeout(),
			"the runtime must advertise a request timeout")
	})
}

func TestNRIVersionExchange(t *testing.T) {
	t.Run("should report a usable NRI version", func(t *testing.T) {
		version := framework.RuntimeNRIVersion()
		if version == "" || version == api.UnknownVersion {
			t.Skipf("runtime %s %s neither advertises nor allows inferring an NRI version",
				framework.RuntimeName(), framework.RuntimeVersion())
		}

		if version == api.DevelVersion {
			t.Logf("runtime reports a development NRI version: %s", version)

			return
		}

		require.True(t, semver.IsValid(version),
			"NRI version %q should be valid semver", version)
	})

	t.Run("should agree with the version inferred from the runtime version", func(t *testing.T) {
		advertised := framework.RuntimeNRIVersion()
		if advertised == "" || advertised == api.UnknownVersion || advertised == api.DevelVersion {
			t.Skipf("runtime NRI version is %q; nothing to cross-check", advertised)
		}

		inferred, err := api.InferVersionFromRuntime(
			framework.RuntimeName(), framework.RuntimeVersion())
		if err != nil {
			t.Skipf("cannot infer an NRI version for %s %s: %v",
				framework.RuntimeName(), framework.RuntimeVersion(), err)
		}

		// The advertised version is authoritative. A mismatch means the table in
		// pkg/api/version.go is stale, which is worth reporting but is not a
		// runtime conformance failure.
		if semver.Compare(semver.MajorMinor(advertised), semver.MajorMinor(inferred)) != 0 {
			t.Logf("advertised NRI version %s differs from the version inferred "+
				"from %s %s (%s); the runtimeVersionMap in pkg/api/version.go may "+
				"need updating",
				advertised, framework.RuntimeName(), framework.RuntimeVersion(), inferred)
		}
	})
}
