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
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListingOnly(t *testing.T) {
	f := flag.Lookup("test.list")
	require.NotNil(t, f, "the testing package registers test.list")

	orig := f.Value.String()
	t.Cleanup(func() { require.NoError(t, f.Value.Set(orig)) })

	require.NoError(t, f.Value.Set(""))
	require.False(t, ListingOnly(), "an empty -test.list is not a listing run")

	require.NoError(t, f.Value.Set(".*"))
	require.True(t, ListingOnly(), "-test.list=.* is a listing run")
}

func TestListingOnlyWithoutFlag(t *testing.T) {
	fs := flag.NewFlagSet("bare", flag.ContinueOnError)
	require.False(t, listingOnly(fs), "a set without test.list is not a listing run")
}

func TestSelectedTests(t *testing.T) {
	// What a fragment records as its selection, and what the status document
	// qualifies its negative claims with, so an unset flag has to read as an
	// unfiltered run rather than as a filter nobody set.
	fs := flag.NewFlagSet("suite", flag.ContinueOnError)
	fs.String("test.run", "", "")

	require.Equal(t, "", selectedTestsIn(fs), "an unset -test.run selects every test")

	require.NoError(t, fs.Set("test.run", "TestInput"))
	require.Equal(t, "TestInput", selectedTestsIn(fs))
}

func TestSelectedTestsWithoutFlag(t *testing.T) {
	fs := flag.NewFlagSet("bare", flag.ContinueOnError)
	require.Equal(t, "", selectedTestsIn(fs), "a set without test.run selects every test")
}
