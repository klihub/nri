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

package version

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

var (
	Build      = semver.Build
	Major      = semver.Major
	Prerelease = semver.Prerelease
)

const (
	UnknownVersion = "0.0.0-unknown"
	DevelVersion   = "(devel)"
	NRIModulePath  = "github.com/containerd/nri"
)

// GetFromBuildInfo returns the locally used NRI version using
// the debug/build info provided by the golang runtime. This
// does not produce usable results for plugins hosted in the
// NRI repository or when NRI is subject to a replace directive.
// in go.mod.
func GetFromBuildInfo() string {
	version := UnknownVersion

	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, mod := range bi.Deps {
			if mod.Path != NRIModulePath {
				continue
			}
			if mod.Replace != nil {
				version = mod.Replace.Version
			} else {
				version = mod.Version
			}
		}
	}

	if version == DevelVersion {
		return UnknownVersion
	}

	return version
}

// MajorMinorPatch returns the major.minor.patch prefix of the semantic version v.
func MajorMinorPatch(v string) string {
	return strings.TrimSuffix(strings.TrimSuffix(v, Build(v)), Prerelease(v))
}

// LastStableTagged returns the last stable tagged version less than or equal to v.
func LastStableTagged(v string) (string, error) {
	var (
		preRelease  = Prerelease(v)
		majMinPatch = MajorMinorPatch(v)
	)

	if preRelease == "" {
		return majMinPatch, nil
	}

	if len(preRelease) < 3 || preRelease[0:3] != "-0." {
		return majMinPatch, nil
	}

	split := strings.SplitN(majMinPatch, ".", 3)
	if len(split) != 3 {
		return majMinPatch, fmt.Errorf("unexpected major.minor.patch %q", majMinPatch)
	}

	maj, min, patch := split[0], split[1], split[2]
	p, err := strconv.ParseUint(patch, 10, 32)
	if err != nil {
		return majMinPatch, fmt.Errorf("failed to parse patch %q: %w", patch, err)
	}

	if p > 0 {
		patch = strconv.FormatUint(p-1, 10)
	}

	return maj + "." + min + "." + patch, nil
}
