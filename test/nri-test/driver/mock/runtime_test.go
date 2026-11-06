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

package mock

import (
	"context"
	stdnet "net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

func TestNewRuntime(t *testing.T) {
	t.Run("should listen on a socket of its own", func(t *testing.T) {
		r, err := NewRuntime(&framework.Config{NRISocketPath: filepath.Join(t.TempDir(), "nri.sock")})
		require.NoError(t, err)
		t.Cleanup(func() { _ = r.Close(context.Background()) })

		require.NotEmpty(t, r.SocketPath())

		fi, err := os.Stat(r.SocketPath())
		require.NoError(t, err)
		require.NotZero(t, fi.Mode()&os.ModeSocket, "the path has to be a listening socket")
	})

	// A path it cannot listen on has to fail here, where the message names it,
	// rather than later as a plugin that never manages to register. The parent
	// is a regular file: Adaptation creates a missing directory itself, so a
	// merely absent one is not this case.
	t.Run("should refuse a socket path it cannot listen on", func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "blocker")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))

		_, err := NewRuntime(&framework.Config{NRISocketPath: filepath.Join(blocker, "nri.sock")})
		require.Error(t, err)
		require.ErrorContains(t, err, "nri.sock")
	})

	// Cleanup and Close both reach this, and a run that failed part way
	// reaches it twice.
	t.Run("should close more than once without error", func(t *testing.T) {
		r, err := NewRuntime(&framework.Config{NRISocketPath: filepath.Join(t.TempDir(), "nri.sock")})
		require.NoError(t, err)

		require.NoError(t, r.Close(context.Background()))
		require.NoError(t, r.Close(context.Background()))
	})

	// A plugin directory on the host must not join the run: the suite's
	// plugins are the only ones a conformance result may describe.
	t.Run("should launch no plugins of its own", func(t *testing.T) {
		r, err := NewRuntime(&framework.Config{NRISocketPath: filepath.Join(t.TempDir(), "nri.sock")})
		require.NoError(t, err)
		t.Cleanup(func() { _ = r.Close(context.Background()) })

		entries, err := os.ReadDir(r.pluginPath)
		require.NoError(t, err, "the plugin path has to exist and be ours")
		require.Empty(t, entries, "nothing may be launched from it")
	})
}

// The socket is the one way this runtime can damage a node. Adaptation's
// listener unlinks whatever is at the path before binding, so a mock run
// pointed at a live runtime's socket would steal it and remove it again on
// close, leaving that runtime unreachable to new plugins.
func TestNewRuntimeAndTheSocket(t *testing.T) {
	t.Run("should refuse a socket something is already listening on", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nri.sock")

		first, err := NewRuntime(&framework.Config{NRISocketPath: path})
		require.NoError(t, err)
		t.Cleanup(func() { _ = first.Close(context.Background()) })

		_, err = NewRuntime(&framework.Config{NRISocketPath: path})
		require.Error(t, err, "the live listener must not be unlinked")
		require.ErrorContains(t, err, "nri.sock")

		// And the first runtime still owns it.
		c, err := stdnet.Dial("unix", path)
		require.NoError(t, err, "the socket still has to be the first runtime's")
		_ = c.Close()
	})

	// Without an explicit -nri-socket the default is the node's own
	// /var/run/nri/nri.sock. A driver that creates nothing has no business
	// there, and a run as root would take it from the runtime.
	t.Run("should not use the node's socket path by default", func(t *testing.T) {
		cfg := &framework.Config{NRISocketPath: api.DefaultSocketPath}

		r, err := NewRuntime(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = r.Close(context.Background()) })

		require.NotEqual(t, api.DefaultSocketPath, r.SocketPath())
		require.FileExists(t, r.SocketPath())

		// And the plugins have to be told: they connect to the config's path,
		// so a socket the runtime moved and did not publish is one no plugin
		// can reach.
		require.Equal(t, r.SocketPath(), cfg.NRISocketPath)
	})
}
