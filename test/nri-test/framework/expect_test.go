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
	"context"
	"strconv"
	"testing"
	"time"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestExpectAnnotation(t *testing.T) {
	spec := &rspec.Spec{Annotations: map[string]string{"key": "value"}}

	t.Run("should accept a present annotation", func(t *testing.T) {
		require.NoError(t, ExpectAnnotation(spec, "key", "value"))
	})

	t.Run("should reject a wrong value and say both", func(t *testing.T) {
		err := ExpectAnnotation(spec, "key", "other")
		require.Error(t, err)
		require.Contains(t, err.Error(), "value")
		require.Contains(t, err.Error(), "other")
	})

	t.Run("should reject a missing annotation", func(t *testing.T) {
		require.Error(t, ExpectAnnotation(spec, "absent", "value"))
	})

	t.Run("should reject a nil spec rather than panic", func(t *testing.T) {
		require.Error(t, ExpectAnnotation(nil, "key", "value"))
	})

	t.Run("should name the annotation it looked for", func(t *testing.T) {
		// Every error names its subject: without the key, a failure in a test that
		// adjusts several annotations does not say which one went missing.
		require.ErrorContains(t, ExpectAnnotation(spec, "absent", "value"), "absent")
		require.ErrorContains(t, ExpectAnnotation(spec, "key", "other"), "key")
		require.ErrorContains(t, ExpectAnnotation(nil, "key", "value"), "key")
	})

	t.Run("should reject a spec with no annotations at all", func(t *testing.T) {
		require.Error(t, ExpectAnnotation(&rspec.Spec{}, "key", "value"))
	})

	t.Run("should report a missing annotation as absent, not as an empty one", func(t *testing.T) {
		// Two different diagnoses, and only one can be true: "the runtime wrote
		// nothing" and "the runtime wrote an empty value" are different bugs. Without
		// its own branch, a missing annotation reports as `has "" wanted "value"`,
		// sending a reader to look for a value that was never written at all.
		absent := ExpectAnnotation(spec, "absent", "value")
		require.ErrorContains(t, absent, "not set in the container's OCI spec")
		require.NotContains(t, absent.Error(), `has ""`,
			"an absent annotation must not be reported as an empty one")

		// And the other side of the distinction: an annotation that really is empty
		// must still report as one, so the two cases stay distinguishable.
		empty := &rspec.Spec{Annotations: map[string]string{"key": ""}}
		require.ErrorContains(t, ExpectAnnotation(empty, "key", "value"), `has ""`)
	})

	t.Run("should list the annotations the spec does carry", func(t *testing.T) {
		// The listing is the diagnosis: a reader sees a misspelled key, or an empty
		// list meaning the adjustment never arrived, without a second run.
		require.ErrorContains(t, ExpectAnnotation(spec, "absent", "value"),
			"the spec carries [key]")
		require.ErrorContains(t, ExpectAnnotation(&rspec.Spec{}, "key", "value"),
			"the spec carries []")
	})
}

func TestExpectEnv(t *testing.T) {
	spec := &rspec.Spec{Process: &rspec.Process{Env: []string{"PATH=/bin", "K=V"}}}
	inside := "PATH=/bin\x00K=V\x00"

	t.Run("should accept env present in both surfaces", func(t *testing.T) {
		require.NoError(t, ExpectEnv(spec, inside, "K", "V"))
	})

	t.Run("should reject env the spec records but the kernel does not have", func(t *testing.T) {
		err := ExpectEnv(spec, "PATH=/bin\x00", "K", "V")
		require.Error(t, err)
		require.Contains(t, err.Error(), "process environment",
			"the message must say which surface disagreed")
	})

	t.Run("should reject env the kernel has but the spec does not record", func(t *testing.T) {
		bare := &rspec.Spec{Process: &rspec.Process{Env: []string{"PATH=/bin"}}}
		require.Error(t, ExpectEnv(bare, inside, "K", "V"))
	})

	t.Run("should reject a nil process rather than panic", func(t *testing.T) {
		require.Error(t, ExpectEnv(&rspec.Spec{}, inside, "K", "V"))
	})

	t.Run("should reject a nil spec rather than panic", func(t *testing.T) {
		require.Error(t, ExpectEnv(nil, inside, "K", "V"))
	})

	t.Run("should name the surface that disagreed, and only that one", func(t *testing.T) {
		// The two surfaces have to be distinguishable in the message. A single
		// generic "K=V not found" leaves the reader unable to tell a runtime that
		// dropped the adjustment from one that recorded it and did not apply it --
		// which are different bugs in different components.
		kernel := ExpectEnv(spec, "PATH=/bin\x00", "K", "V")
		require.ErrorContains(t, kernel, "process environment")
		require.NotContains(t, kernel.Error(), "OCI spec",
			"the spec had it; the message must not blame the spec")

		bare := &rspec.Spec{Process: &rspec.Process{Env: []string{"PATH=/bin"}}}
		recorded := ExpectEnv(bare, inside, "K", "V")
		require.ErrorContains(t, recorded, "OCI spec")
		require.NotContains(t, recorded.Error(), "process environment",
			"the kernel had it; the message must not blame the kernel")
	})

	t.Run("should report the key and the wanted value", func(t *testing.T) {
		err := ExpectEnv(spec, inside, "K", "other")
		require.Error(t, err)
		require.ErrorContains(t, err, "K")
		require.ErrorContains(t, err, "other")
	})

	t.Run("should not match a key that merely shares a prefix", func(t *testing.T) {
		// A substring search would let KK=V satisfy an expectation of K=V, and the
		// assertion would pass with the adjustment unapplied.
		//
		// Each surface is exercised on its own, with the other surface given the entry
		// and the message required to name the dissenter. Asserting only "an error came
		// back" would be satisfied by whichever surface happened to reject, leaving a
		// substring match on the other one invisible -- which is exactly what happened
		// to the first version of this subtest.
		exact := &rspec.Spec{Process: &rspec.Process{Env: []string{"K=V"}}}

		prefixed := &rspec.Spec{Process: &rspec.Process{Env: []string{"KK=V"}}}
		require.ErrorContains(t, ExpectEnv(prefixed, "K=V\x00KK=V\x00", "K", "V"), "OCI spec")
		require.ErrorContains(t, ExpectEnv(exact, "KK=V\x00", "K", "V"), "process environment")

		suffixed := &rspec.Spec{Process: &rspec.Process{Env: []string{"K=VV"}}}
		require.ErrorContains(t, ExpectEnv(suffixed, "K=V\x00K=VV\x00", "K", "V"), "OCI spec")
		require.ErrorContains(t, ExpectEnv(exact, "K=VV\x00", "K", "V"), "process environment")
	})

	t.Run("should list what each surface does have", func(t *testing.T) {
		// The listing is the diagnosis, on whichever surface dissented: PATH=/bin and
		// nothing else says the adjustment never arrived, and says it without a second
		// run against a container that no longer exists.
		bare := &rspec.Spec{Process: &rspec.Process{Env: []string{"PATH=/bin"}}}
		require.ErrorContains(t, ExpectEnv(bare, inside, "K", "V"), "it records [PATH=/bin]")
		require.ErrorContains(t, ExpectEnv(spec, "PATH=/bin\x00", "K", "V"),
			"the process has [PATH=/bin]")
	})

	t.Run("should accept an empty value in both surfaces", func(t *testing.T) {
		// An emptied variable is a legitimate adjustment, and the NUL-separated
		// environ ends in a separator, so a parser that dropped empty entries
		// carelessly could lose the last variable too.
		empty := &rspec.Spec{Process: &rspec.Process{Env: []string{"PATH=/bin", "K="}}}
		require.NoError(t, ExpectEnv(empty, "PATH=/bin\x00K=\x00", "K", ""))
	})
}

func TestExpectMount(t *testing.T) {
	spec := &rspec.Spec{Mounts: []rspec.Mount{{Source: "/src", Destination: "/dst"}}}
	mounts := "proc /proc proc rw 0 0\n/dev/sda1 /dst ext4 rw,relatime 0 0\n"

	t.Run("should accept a mount present in both surfaces", func(t *testing.T) {
		require.NoError(t, ExpectMount(spec, mounts, "/dst"))
	})

	t.Run("should reject a mount the spec records but the kernel lacks", func(t *testing.T) {
		require.Error(t, ExpectMount(spec, "proc /proc proc rw 0 0\n", "/dst"))
	})

	t.Run("should reject a destination absent from the spec", func(t *testing.T) {
		require.Error(t, ExpectMount(spec, mounts, "/nowhere"))
	})

	t.Run("should reject a nil spec rather than panic", func(t *testing.T) {
		require.Error(t, ExpectMount(nil, mounts, "/dst"))
	})

	t.Run("should name the surface that disagreed, and only that one", func(t *testing.T) {
		kernel := ExpectMount(spec, "proc /proc proc rw 0 0\n", "/dst")
		require.ErrorContains(t, kernel, "mount table")
		require.NotContains(t, kernel.Error(), "OCI spec")

		recorded := ExpectMount(&rspec.Spec{}, mounts, "/dst")
		require.ErrorContains(t, recorded, "OCI spec")
		require.NotContains(t, recorded.Error(), "mount table")
	})

	t.Run("should name the destination it looked for", func(t *testing.T) {
		require.ErrorContains(t, ExpectMount(spec, mounts, "/nowhere"), "/nowhere")
	})

	t.Run("should read the mount point, not the device or the type", func(t *testing.T) {
		// Field 2 of /proc/self/mounts is the mount point. Reading field 1 would
		// accept "/dst /elsewhere ext4 rw 0 0" -- a filesystem mounted FROM the
		// path under test, which proves the opposite of what is being asserted.
		wrongField := &rspec.Spec{Mounts: []rspec.Mount{{Destination: "/dst"}}}
		require.Error(t, ExpectMount(wrongField, "/dst /elsewhere ext4 rw 0 0\n", "/dst"))
	})

	t.Run("should list what each surface does have", func(t *testing.T) {
		// As with ExpectEnv: the listing is what turns a failure into a diagnosis, and
		// a mount table is the one surface a reader cannot go back and re-read.
		require.ErrorContains(t, ExpectMount(spec, mounts, "/nowhere"), "it records [/dst]")
		require.ErrorContains(t, ExpectMount(spec, "proc /proc proc rw 0 0\n", "/dst"),
			"the container has mounted [/proc]")
	})

	t.Run("should ignore a truncated mount line rather than panic", func(t *testing.T) {
		// The last line of a file read through exec can be short; a line with no
		// second field must be skipped, not indexed into.
		require.NoError(t, ExpectMount(spec, mounts+"/dev/sdb1", "/dst"))
		require.NoError(t, ExpectMount(spec, "\n\n"+mounts, "/dst"))
	})
}

func TestExpectMountOptions(t *testing.T) {
	spec := &rspec.Spec{Mounts: []rspec.Mount{{Destination: "/mnt/m"}}}

	t.Run("should accept an option present in the mount table", func(t *testing.T) {
		// mount present in both surfaces, ro present in the table
		require.NoError(t, ExpectMount(spec, "/dev/x /mnt/m ext4 ro,relatime 0 0", "/mnt/m", "ro"))
	})

	t.Run("should reject an absent option and name the options it has", func(t *testing.T) {
		// ro absent from the table: the runtime honoured the bind and dropped ro
		err := ExpectMount(spec, "/dev/x /mnt/m ext4 rw,relatime 0 0", "/mnt/m", "ro")
		require.ErrorContains(t, err, "ro")
		require.ErrorContains(t, err, "rw,relatime")
	})

	t.Run("should not satisfy an option with a substring of another", func(t *testing.T) {
		// a substring must not satisfy the option
		err := ExpectMount(spec, "/dev/x /mnt/m ext4 errors=remount-ro 0 0", "/mnt/m", "ro")
		require.ErrorContains(t, err, "ro")
	})

	t.Run("should error on a line truncated before the options field", func(t *testing.T) {
		// a line truncated before the options field errors rather than panicking
		err := ExpectMount(spec, "/dev/x /mnt/m ext4", "/mnt/m", "ro")
		require.ErrorContains(t, err, "options")
	})

	t.Run("should check only the two surfaces when no option is wanted", func(t *testing.T) {
		// no wantOpts keeps the existing two-surface behaviour
		require.NoError(t, ExpectMount(spec, "/dev/x /mnt/m ext4 rw 0 0", "/mnt/m"))
	})
}

func TestExpectCpuset(t *testing.T) {
	t.Run("should accept the kernel's allowed list matching", func(t *testing.T) {
		require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "0-1"))
	})

	t.Run("should reject a mismatch and report what the kernel said", func(t *testing.T) {
		err := ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-43\n"), "0")
		require.Error(t, err)
		require.Contains(t, err.Error(), "0-43")
	})

	t.Run("should reject a status with no allowed list", func(t *testing.T) {
		err := ExpectCpuset(ProcStatus("Name:\tsleep\n"), "0")
		require.Error(t, err)
		// Said as an absence, not as a mismatch: a status with no allowed list in it
		// means the read went wrong -- an exec that returned nothing, a parser that
		// dropped every line -- and reporting it as "the kernel allows nothing" would
		// blame cpuset for a broken channel.
		require.ErrorContains(t, err, "reports no Cpus_allowed_list")

		// A status carrying only the sibling key: the cpuset expectation must read
		// Cpus_allowed_list, never the NUMA one.
		require.Error(t, ExpectCpuset(ProcStatus("Mems_allowed_list:\t0\n"), "0"))
	})

	t.Run("should accept an equivalent spelling of the same set", func(t *testing.T) {
		require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "0,1"))
		require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "1,0"))
		require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0,2\n"), "2,0"))
		require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-2,5\n"), "5,0-2"))
	})

	t.Run("should still reject a genuinely different set", func(t *testing.T) {
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "0-2"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-2\n"), "0-1"))

		// Same size, entirely different members. Without these, every case above
		// differs in cardinality, so comparing sizes rather than membership would
		// satisfy this subtest -- and sizes are exactly what a plugin that pinned a
		// container to the wrong socket gets right. That is the failure this whole
		// function exists to catch, so it is asserted rather than implied.
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "2-3"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0,2\n"), "1,3"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t2-3\n"), "0-1"))

		// Overlapping but not equal, same size: the sets share a member, so neither
		// cardinality nor a "shares anything" test would separate them.
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "1-2"))
	})

	t.Run("should reject an unparseable list rather than silently accept it", func(t *testing.T) {
		err := ExpectCpuset(ProcStatus("Cpus_allowed_list:\tbogus\n"), "0")
		require.Error(t, err)
		// Said as an unreadable string, not as a mismatch -- the same distinction the
		// wanted side and the missing-key case make. A mismatch message here would
		// send a reader to the runtime when the fault is in the read.
		require.ErrorContains(t, err, "cannot read Cpus_allowed_list")

		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-\n"), "0"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t3-1\n"), "0"))

		// The same garbage alongside a member that does parse, with the expectation
		// set to what that member alone would give. An implementation that skipped
		// what it could not read rather than failing on it would report success here
		// -- and the three assertions above would not notice, because a partial set
		// differs from what they ask for anyway, so they pass for the wrong reason.
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t3-1,5\n"), "5"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\tbogus,5\n"), "5"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-,5\n"), "5"))
	})

	t.Run("should name cpus as its subject", func(t *testing.T) {
		err := ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "0")
		require.ErrorContains(t, err, "Cpus_allowed_list")
		require.ErrorContains(t, err, "cpuset",
			"the subject, not just the key, so a failure reads without /proc to hand")
	})
}

func TestExpectMems(t *testing.T) {
	// The same subtests as ExpectCpuset: the two are wrappers over one helper
	// precisely so that they cannot drift apart, and asserting the same properties
	// of both is what makes that visible if anyone ever splits them.
	t.Run("should accept the kernel's allowed list matching", func(t *testing.T) {
		require.NoError(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "0-1"))
	})

	t.Run("should reject a mismatch and report what the kernel said", func(t *testing.T) {
		err := ExpectMems(ProcStatus("Mems_allowed_list:\t0-3\n"), "0")
		require.Error(t, err)
		require.Contains(t, err.Error(), "0-3")
	})

	t.Run("should reject a status with no allowed list", func(t *testing.T) {
		err := ExpectMems(ProcStatus("Name:\tsleep\n"), "0")
		require.Error(t, err)
		require.ErrorContains(t, err, "reports no Mems_allowed_list")

		// A status carrying only the sibling key: the NUMA expectation must read
		// Mems_allowed_list, never the cpuset one. This is the copy-paste bug the
		// shared helper is meant to prevent, so it is asserted rather than assumed.
		require.Error(t, ExpectMems(ProcStatus("Cpus_allowed_list:\t0\n"), "0"))
	})

	t.Run("should accept an equivalent spelling of the same set", func(t *testing.T) {
		require.NoError(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "0,1"))
		require.NoError(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "1,0"))
		require.NoError(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0,2\n"), "2,0"))
		require.NoError(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-2,5\n"), "5,0-2"))
	})

	t.Run("should still reject a genuinely different set", func(t *testing.T) {
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "0-2"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-2\n"), "0-1"))

		// Same size, different members, as in the cpuset case and for the same reason:
		// the wrong NUMA node is a set of the right size. A plugin moving a container
		// to the wrong socket is the motivating bug for reading these lists at all.
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "2-3"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0,2\n"), "1,3"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t1\n"), "0"))
	})

	t.Run("should reject an unparseable list rather than silently accept it", func(t *testing.T) {
		err := ExpectMems(ProcStatus("Mems_allowed_list:\tbogus\n"), "0")
		require.Error(t, err)
		require.ErrorContains(t, err, "cannot read Mems_allowed_list")

		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-\n"), "0"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t3-1\n"), "0"))

		// Garbage beside a member that parses, as in the cpuset case and for the same
		// reason: skipping what cannot be read must not pass for success.
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t3-1,5\n"), "5"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\tbogus,5\n"), "5"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-,5\n"), "5"))
	})

	t.Run("should name NUMA nodes as its subject", func(t *testing.T) {
		err := ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "0")
		require.ErrorContains(t, err, "Mems_allowed_list")
		require.ErrorContains(t, err, "NUMA",
			"the subject, not just the key: the two wrappers must not be confusable")
	})
}

func TestExpectAllowedList(t *testing.T) {
	// The properties shared by both allowed-list expectations, asserted once
	// against the helper they share.
	t.Run("should reject an unparseable expectation, not just an unparseable status", func(t *testing.T) {
		// The dangerous direction: a want the parser could not read must never be
		// treated as the empty set, because an empty set compares equal to another
		// empty set and the test would pass with neither side understood.
		err := ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "bogus")
		require.Error(t, err)
		// Reported as an unreadable expectation, not as a mismatch. The distinction is
		// the whole diagnosis: a mismatch message would send a reader to the runtime,
		// when the fault is in the test's own argument.
		require.ErrorContains(t, err, "cannot read the wanted")

		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), "0-"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-1\n"), ""))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), "bogus"))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-1\n"), ""))
	})

	t.Run("should not let two unparseable sides agree", func(t *testing.T) {
		// Both sides garbage is the failure mode fail-closed parsing exists for: an
		// empty-set parser would report success here.
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\tbogus\n"), "bogus"))
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t\n"), ""))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\tbogus\n"), "bogus"))
	})

	t.Run("should reject a nil status rather than panic", func(t *testing.T) {
		require.Error(t, ExpectCpuset(nil, "0"))
		require.Error(t, ExpectMems(nil, "0"))
	})

	t.Run("should report both the raw text and the parsed set on a mismatch", func(t *testing.T) {
		// Raw, because that is what a reader compares against the runtime's logs;
		// parsed, because that is what the comparison actually used, and a reader
		// chasing a spelling problem needs to see both.
		err := ExpectCpuset(ProcStatus("Cpus_allowed_list:\t1,0,2\n"), "0-1")
		require.Error(t, err)
		require.ErrorContains(t, err, "1,0,2", "the kernel's raw text must be reported")
		require.ErrorContains(t, err, "0-2", "the parsed set must be reported too")
		require.ErrorContains(t, err, "0-1", "the expectation must be reported")
	})

	t.Run("should reject an id beyond any plausible cpu or node number", func(t *testing.T) {
		// A typo in a range bound would otherwise ask for a set of a hundred million
		// members and spend the run's memory building it. Asserted with the two sides
		// naming the same out-of-bounds id, so that dropping the bound shows up as a
		// failure here rather than merely as a slow, enormous comparison.
		beyond := strconv.Itoa(maxAllowedListID + 1)
		require.Error(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t"+beyond+"\n"), beyond))
		require.Error(t, ExpectMems(ProcStatus("Mems_allowed_list:\t0-"+beyond+"\n"), "0-"+beyond))
	})

	t.Run("should reject the syntaxes that look like numbers but are not", func(t *testing.T) {
		// Each spelling is paired with the expectation it would match if the parser
		// reinterpreted it as a number, so none of these assertions can pass merely
		// because the two sides came out as different sets.
		for _, bad := range []struct{ list, want string }{
			{"+0", "0"}, {"+1", "1"}, {"-1", "1"}, {"0x1", "1"}, {"1.0", "1"},
			{"1-2-3", "1-3"}, {"0,,1", "0-1"}, {"1,", "1"}, {",1", "1"}, {"0 1", "0-1"},
			// All digits, so it passes the digit check and reaches strconv.Atoi, which
			// rejects it for overflow -- any decimal of 19 or more digits does. Paired
			// with "0" deliberately: 0 is what Atoi returns alongside that error, so an
			// implementation that ignored the error would match this expectation.
			{"9999999999999999999999999", "0"},
		} {
			require.Error(t,
				ExpectCpuset(ProcStatus("Cpus_allowed_list:\t"+bad.list+"\n"), bad.want),
				"%q must not parse", bad.list)
		}
	})

	t.Run("should accept the spellings the kernel and a test author use", func(t *testing.T) {
		for _, spelling := range []string{"0-3", "0,1,2,3", "3,2,1,0", "0-1,2-3", "0,1-2,3"} {
			require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t0-3\n"), spelling),
				"%q must parse to 0-3", spelling)
		}

		// A single-member range is what a kernel prints for one cpu, and a
		// degenerate range is what a script generating a list prints.
		require.NoError(t, ExpectCpuset(ProcStatus("Cpus_allowed_list:\t7\n"), "7-7"))
	})
}

// specInspector is an Inspector returning a programmed spec. It embeds
// fakeExecutor so that one fake can stand in for the CRI inspector, which is
// both surfaces at once.
type specInspector struct {
	fakeExecutor

	spec *rspec.Spec
	err  error

	inspected []string
}

func (i *specInspector) Inspect(_ context.Context, id string) (*Observed, error) {
	i.inspected = append(i.inspected, id)

	if i.err != nil {
		return nil, i.err
	}

	return &Observed{Spec: i.spec, State: StateRunning}, nil
}

var _ Inspector = &specInspector{}

// expectSuite wires a Suite to ft with i as both the inspector and the executor,
// and returns both.
func expectSuite(t *testing.T, i *specInspector) (*Suite, *fakeT) {
	t.Helper()

	useDriver(t, &fakeDriver{})
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})
	useInspector(t, i)
	useExecutor(t, &i.fakeExecutor)

	ft := &fakeT{}

	return newFakeSuite(ft), ft
}

func TestSuiteExpectAnnotation(t *testing.T) {
	ctr := &Container{ID: "ctr0"}

	t.Run("reads the spec and passes a matching annotation", func(t *testing.T) {
		i := &specInspector{spec: &rspec.Spec{Annotations: map[string]string{"k": "v"}}}
		s, ft := expectSuite(t, i)

		s.ExpectAnnotation(ctr, "k", "v")

		require.Equal(t, []string{"ctr0"}, i.inspected)
		require.Empty(t, ft.errors)
		require.Empty(t, i.cmds, "an annotation has no in-container expression to exec for")
	})

	t.Run("fails the case naming the container when it does not match", func(t *testing.T) {
		i := &specInspector{spec: &rspec.Spec{Annotations: map[string]string{"k": "v"}}}
		s, ft := expectSuite(t, i)

		require.Panics(t, func() { s.ExpectAnnotation(ctr, "k", "other") })
		require.Contains(t, ft.allErrors(), "ctr0")
		require.Contains(t, ft.allErrors(), "other")
	})
}

func TestSuiteExpectEnv(t *testing.T) {
	ctr := &Container{ID: "ctr0"}

	t.Run("reads both surfaces and passes when they agree", func(t *testing.T) {
		i := &specInspector{spec: &rspec.Spec{Process: &rspec.Process{Env: []string{"K=V"}}}}
		i.stdout = "K=V\x00"
		s, ft := expectSuite(t, i)

		s.ExpectEnv(ctr, "K", "V")

		require.Equal(t, []string{"ctr0"}, i.inspected)
		require.Equal(t, [][]string{{"cat", "/proc/self/environ"}}, i.cmds,
			"the process's own environment is read from /proc/self/environ")
		require.Empty(t, ft.errors)
	})

	t.Run("fails the case when the kernel disagrees with the spec", func(t *testing.T) {
		i := &specInspector{spec: &rspec.Spec{Process: &rspec.Process{Env: []string{"K=V"}}}}
		i.stdout = "PATH=/bin\x00"
		s, ft := expectSuite(t, i)

		require.Panics(t, func() { s.ExpectEnv(ctr, "K", "V") })
		require.Contains(t, ft.allErrors(), "process environment")
		require.Contains(t, ft.allErrors(), "ctr0")
	})
}

func TestSuiteExpectMount(t *testing.T) {
	ctr := &Container{ID: "ctr0"}

	t.Run("reads both surfaces and passes when they agree", func(t *testing.T) {
		i := &specInspector{spec: &rspec.Spec{Mounts: []rspec.Mount{{Destination: "/dst"}}}}
		i.stdout = "/dev/sda1 /dst ext4 rw 0 0\n"
		s, ft := expectSuite(t, i)

		s.ExpectMount(ctr, "/dst")

		require.Equal(t, []string{"ctr0"}, i.inspected)
		require.Equal(t, [][]string{{"cat", "/proc/self/mounts"}}, i.cmds,
			"the process's own mount table is read from /proc/self/mounts")
		require.Empty(t, ft.errors)
	})

	t.Run("fails the case when the mount is not there", func(t *testing.T) {
		i := &specInspector{spec: &rspec.Spec{Mounts: []rspec.Mount{{Destination: "/dst"}}}}
		i.stdout = "proc /proc proc rw 0 0\n"
		s, ft := expectSuite(t, i)

		require.Panics(t, func() { s.ExpectMount(ctr, "/dst") })
		require.Contains(t, ft.allErrors(), "/dst")
		require.Contains(t, ft.allErrors(), "ctr0")
	})
}

func TestSuiteExpectAllowedLists(t *testing.T) {
	ctr := &Container{ID: "ctr0"}

	t.Run("reads the cpuset from /proc/self/status", func(t *testing.T) {
		i := &specInspector{}
		i.stdout = "Cpus_allowed_list:\t0-1\nMems_allowed_list:\t0\n"
		s, ft := expectSuite(t, i)

		s.ExpectCpuset(ctr, "1,0")

		require.Equal(t, [][]string{{"cat", "/proc/self/status"}}, i.cmds)
		require.Empty(t, i.inspected, "the kernel alone is authoritative; no spec is needed")
		require.Empty(t, ft.errors)
	})

	t.Run("reads the NUMA nodes from /proc/self/status", func(t *testing.T) {
		i := &specInspector{}
		i.stdout = "Cpus_allowed_list:\t0-1\nMems_allowed_list:\t0-1\n"
		s, ft := expectSuite(t, i)

		s.ExpectMems(ctr, "1,0")

		require.Equal(t, [][]string{{"cat", "/proc/self/status"}}, i.cmds)
		require.Empty(t, i.inspected)
		require.Empty(t, ft.errors)
	})

	t.Run("fails the case naming the container on a mismatch", func(t *testing.T) {
		i := &specInspector{}
		i.stdout = "Cpus_allowed_list:\t0-43\nMems_allowed_list:\t0-1\n"
		s, ft := expectSuite(t, i)

		require.Panics(t, func() { s.ExpectCpuset(ctr, "0") })
		require.Contains(t, ft.allErrors(), "0-43")
		require.Contains(t, ft.allErrors(), "ctr0")
	})

	t.Run("fails the case when the NUMA nodes do not match", func(t *testing.T) {
		i := &specInspector{}
		i.stdout = "Cpus_allowed_list:\t0-1\nMems_allowed_list:\t0-1\n"
		s, ft := expectSuite(t, i)

		require.Panics(t, func() { s.ExpectMems(ctr, "0") })
		require.Contains(t, ft.allErrors(), "Mems_allowed_list")
		require.Contains(t, ft.allErrors(), "ctr0")
	})
}
