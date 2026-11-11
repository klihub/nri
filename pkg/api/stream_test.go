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

package api

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// oneofArms returns the names of the fields in the msg oneof of a message.
func oneofArms(t *testing.T, md protoreflect.MessageDescriptor) []string {
	oneof := md.Oneofs().ByName("msg")
	require.NotNil(t, oneof, "%s has no msg oneof", md.Name())

	var arms []string
	for i := 0; i < oneof.Fields().Len(); i++ {
		arms = append(arms, string(oneof.Fields().Get(i).Name()))
	}

	sort.Strings(arms)
	return arms
}

// answeredByStub lists the RuntimeMessage request arms that the stub's
// answerStream has a case for.
var answeredByStub = []string{
	"configure_request",
	"create_container_request",
	"post_create_container_request",
	"post_start_container_request",
	"post_update_container_request",
	"post_update_pod_sandbox_request",
	"remove_container_request",
	"remove_pod_sandbox_request",
	"run_pod_sandbox_request",
	"shutdown_request",
	"start_container_request",
	"stop_container_request",
	"stop_pod_sandbox_request",
	"synchronize_request",
	"update_container_request",
	"update_pod_sandbox_request",
	"validate_container_adjustment_request",
}

func TestEveryStreamedRequestHasAnAnswer(t *testing.T) {
	// A new arm in the proto with no case in answerStream is a request
	// the plugin silently fails. The compiler cannot see it: the arm is
	// an interface implementation, not a switch the compiler checks.
	var requests []string
	for _, arm := range oneofArms(t, (&RuntimeMessage{}).ProtoReflect().Descriptor()) {
		if strings.HasSuffix(arm, "_request") {
			requests = append(requests, arm)
		}
	}

	require.Equal(t, answeredByStub, requests,
		"RuntimeMessage request arms differ from the cases of answerStream")
}

func TestEnvelopesCarryBothDirections(t *testing.T) {
	rt := (&RuntimeMessage{}).ProtoReflect().Descriptor()
	pl := (&PluginMessage{}).ProtoReflect().Descriptor()

	for _, arm := range []string{
		"register_plugin_response",
		"update_containers_response",
	} {
		require.NotNil(t, rt.Fields().ByName(protoreflect.Name(arm)),
			"RuntimeMessage has no %s", arm)
	}
	require.NotNil(t, rt.Fields().ByName("error"), "RuntimeMessage has no error")

	for _, arm := range []string{
		"register_plugin_request",
		"update_containers_request",
	} {
		require.NotNil(t, pl.Fields().ByName(protoreflect.Name(arm)),
			"PluginMessage has no %s", arm)
	}
}
