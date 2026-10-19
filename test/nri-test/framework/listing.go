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

import "flag"

// ListingOnly reports whether this run only lists tests, as with
// -test.list=<regexp>. Such a run prints test names and exits.
//
// The suite's TestMain consults it before Setup, so that -test.list works on a
// node with no root, no container runtime and no NRI socket. Setup dials CRI,
// connects a plugin and pulls an image; none of that is needed to print names.
func ListingOnly() bool {
	return listingOnly(flag.CommandLine)
}

// listingOnly is ListingOnly over an arbitrary flag set, so that the case
// where no test.list flag is registered, i.e. not a test binary, can be tested.
func listingOnly(fs *flag.FlagSet) bool {
	f := fs.Lookup("test.list")
	if f == nil {
		return false
	}

	return f.Value.String() != ""
}
