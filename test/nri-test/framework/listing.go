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
// -test.list=<regexp>. Such a run prints test names and exits, so it needs
// neither root, nor a container runtime, nor an NRI socket.
func ListingOnly() bool {
	return listingOnly(flag.CommandLine)
}

// listingOnly is ListingOnly over an arbitrary flag set, including one that
// registers no test.list flag at all.
func listingOnly(fs *flag.FlagSet) bool {
	f := fs.Lookup("test.list")
	if f == nil {
		return false
	}

	return f.Value.String() != ""
}
