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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func useExtraImages(t *testing.T, images ...string) {
	t.Helper()

	imagesMu.Lock()
	saved := extraImage
	extraImage = nil
	imagesMu.Unlock()

	t.Cleanup(func() {
		imagesMu.Lock()
		extraImage = saved
		imagesMu.Unlock()
	})

	for _, i := range images {
		UseImage(i)
	}
}

func TestImages(t *testing.T) {
	t.Run("lists the default image first, then declared ones sorted", func(t *testing.T) {
		useExtraImages(t, "z/img:1", "a/img:1", "z/img:1", "")

		require.Equal(t, []string{"def:1", "a/img:1", "z/img:1"},
			Images(&Config{Image: "def:1"}))
	})

	t.Run("does not repeat the default image", func(t *testing.T) {
		useExtraImages(t, "def:1")

		require.Equal(t, []string{"def:1"}, Images(&Config{Image: "def:1"}))
	})

	t.Run("falls back to the built-in default without a complete config", func(t *testing.T) {
		useExtraImages(t)

		require.Equal(t, []string{DefaultImage}, Images(&Config{}))
	})

	t.Run("prints one per line", func(t *testing.T) {
		useExtraImages(t, "a/img:1")

		var b bytes.Buffer
		ListImages(&b, &Config{Image: "def:1"})
		require.Equal(t, "def:1\na/img:1\n", b.String())
	})
}

func TestPullImages(t *testing.T) {
	cfg := &Config{Image: "def:1", Timeout: time.Minute}

	t.Run("pulls every image and closes the driver", func(t *testing.T) {
		useExtraImages(t, "a/img:1")

		d := &pullDriver{}
		factory := func(context.Context, *Config) (Driver, Inspector, error) { return d, nil, nil }

		require.NoError(t, PullImages(cfg, factory))
		require.Equal(t, []string{"def:1", "a/img:1"}, d.pulled)
	})

	t.Run("tries the rest after a failure and reports it", func(t *testing.T) {
		useExtraImages(t, "a/img:1")

		d := &pullDriver{err: errors.New("no such host")}
		factory := func(context.Context, *Config) (Driver, Inspector, error) { return d, nil, nil }

		err := PullImages(cfg, factory)
		require.ErrorContains(t, err, "def:1")
		require.ErrorContains(t, err, "a/img:1")
		require.Len(t, d.pulled, 2)
	})

	t.Run("refuses a driver that cannot pull", func(t *testing.T) {
		useExtraImages(t)

		factory := func(context.Context, *Config) (Driver, Inspector, error) { return &noOpDriver{}, nil, nil }

		require.ErrorContains(t, PullImages(cfg, factory), "cannot pull images")
	})
}
