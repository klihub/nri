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

package adaptation_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEventStreamNegotiation pins which transport a plugin ends up on.
//
// Without it the rest of the streaming measurements prove nothing: a run
// that quietly fell back to unary calls looks exactly like one that
// streamed, and the numbers would be the same either way for the wrong
// reason.
func TestEventStreamNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want bool
	}{
		{
			name: "streams when nothing says otherwise",
			env:  "",
			want: true,
		},
		{
			name: "uses unary calls when the plugin is told not to stream",
			env:  "0",
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NRI_STREAMING", tc.env)

			rig := startRig(t, 1, fixedDelay(0))

			require.Equal(t, tc.want, rig.plugins[0].stub.Streaming(),
				"plugin's view of whether it is streaming")
		})
	}
}
