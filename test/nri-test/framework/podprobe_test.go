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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runcSystemdRefusal is what the first pod creation failed with against a
// containerd whose runc handler had SystemdCgroup set, measured on Fedora 43.
const runcSystemdRefusal = `failed to run pod sandbox nri-test-adjust-env-pod-1-2: ` +
	`rpc error: code = Unknown desc = failed to start sandbox "acc34fd4d2": ` +
	`failed to create containerd task: failed to create shim task: OCI runtime ` +
	`create failed: runc create failed: expected cgroupsPath to be of format ` +
	`"slice:prefix:name" for systemd cgroups, got "/k8s.io/acc34fd4d2" instead`

// TestASystemdCgroupRefusalNamesTheFlagThatFixesIt pins that the one refusal a
// reader cannot act on from its own text names the flag that resolves it.
func TestASystemdCgroupRefusalNamesTheFlagThatFixesIt(t *testing.T) {
	err := diagnosePodCreation(errors.New(runcSystemdRefusal), &Config{})
	require.Error(t, err)

	for _, want := range []string{
		"expects a systemd cgroup path",
		"-nri-test.cgroup-parent=system.slice",
		runcSystemdRefusal,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnosis does not carry %q: %v", want, err)
		}
	}
}

// TestARefusalWithACgroupParentIsNotDiagnosedAsOne pins that a run that
// already named a parent is not told to name one: the cause is then something
// else, and the suggestion would send a reader after the wrong thing.
func TestARefusalWithACgroupParentIsNotDiagnosedAsOne(t *testing.T) {
	err := diagnosePodCreation(errors.New(runcSystemdRefusal),
		&Config{CgroupParent: "system.slice"})
	require.Error(t, err)

	if strings.Contains(err.Error(), "-nri-test.cgroup-parent") {
		t.Errorf("a run that named a parent is told to name one: %v", err)
	}
	if !strings.Contains(err.Error(), runcSystemdRefusal) {
		t.Errorf("the runtime's own error is lost: %v", err)
	}
}

// TestAnUnrelatedRefusalIsPassedThrough pins that a refusal with another cause
// keeps its own message and gains no guess about cgroups.
func TestAnUnrelatedRefusalIsPassedThrough(t *testing.T) {
	err := diagnosePodCreation(errors.New("no such image"), &Config{})
	require.Error(t, err)

	if strings.Contains(err.Error(), "cgroup") {
		t.Errorf("an unrelated refusal is diagnosed as a cgroup problem: %v", err)
	}
	if !strings.Contains(err.Error(), "no such image") {
		t.Errorf("the runtime's own error is lost: %v", err)
	}
}

// TestADiagnosisUnwrapsToTheRuntimesError pins that the wrapped error is still
// reachable, so a caller can inspect it rather than only print it.
func TestADiagnosisUnwrapsToTheRuntimesError(t *testing.T) {
	cause := errors.New(runcSystemdRefusal)
	require.ErrorIs(t, diagnosePodCreation(cause, &Config{}), cause)
	require.ErrorIs(t, diagnosePodCreation(cause, &Config{CgroupParent: "x"}), cause)
}
