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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilityHas(t *testing.T) {
	t.Run("should report a single present capability", func(t *testing.T) {
		c := CapContainerLifecycle | CapExec
		require.True(t, c.Has(CapContainerLifecycle))
		require.True(t, c.Has(CapExec))
		require.False(t, c.Has(CapUpdatePodResources))
	})

	t.Run("should require all requested capabilities", func(t *testing.T) {
		c := CapContainerLifecycle | CapExec
		require.True(t, c.Has(CapContainerLifecycle|CapExec))
		require.False(t, c.Has(CapContainerLifecycle|CapUpdatePodResources))
	})

	t.Run("should treat no requested capability as satisfied", func(t *testing.T) {
		require.True(t, Capability(0).Has(0))
	})
}

func TestCapabilityString(t *testing.T) {
	t.Run("should name the present capabilities", func(t *testing.T) {
		require.Equal(t, "none", Capability(0).String())
		require.Equal(t, "ContainerLifecycle", CapContainerLifecycle.String())
		require.Equal(t,
			"ContainerLifecycle,Exec",
			(CapContainerLifecycle | CapExec).String(),
		)
	})
}
