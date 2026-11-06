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

package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/containerd/nri/test/nri-test/framework"
)

// Key identifies the runtime configuration a fragment describes.
type Key struct {
	Runtime, Version, Handler string
}

// KeyOf returns the Key of f.
func KeyOf(f framework.Fragment) Key {
	return Key{
		Runtime: f.Header.Runtime,
		Version: f.Header.RuntimeVersion,
		Handler: f.Header.RuntimeHandler,
	}
}

// Load returns the fragments in the *.json files of dir, ordered by Key. It
// fails on a dir that is not a directory, on a file that is unreadable, has an
// unknown schema, was not written by a CRI driver run or names no runtime, and
// on two files with the same Key.
//
// A directory with no fragment in it is not an error, which is a fresh tree; a
// directory that is not there is, since the two render alike and only one of
// them is a state anyone meant to be in.
func Load(dir string) ([]framework.Fragment, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%w; a run of the suite writes a fragment there "+
			"when it is given -nri-test.status-dir", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory of fragments", dir)
	}

	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}

	files := map[Key]string{}
	frags := []framework.Fragment{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		var head struct {
			Schema int `json:"schema"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if head.Schema != framework.FragmentSchema {
			return nil, fmt.Errorf("%s: schema %d, want %d",
				path, head.Schema, framework.FragmentSchema)
		}

		var f framework.Fragment
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if f.Environment.Driver != framework.DriverCRI {
			return nil, fmt.Errorf("%s: driver %q, want %q",
				path, f.Environment.Driver, framework.DriverCRI)
		}

		// A run whose Setup failed before it read the runtime's version still
		// writes a fragment, deliberately. Such a fragment names no column,
		// and a nameless column in the document reads as a formatting bug
		// rather than as the run that produced it.
		if f.Header.Runtime == "" || f.Header.RuntimeVersion == "" {
			return nil, fmt.Errorf("%s: runtime %q version %q: a fragment names "+
				"both, and a run whose setup failed before reading them names neither",
				path, f.Header.Runtime, f.Header.RuntimeVersion)
		}

		if other, ok := files[KeyOf(f)]; ok {
			return nil, fmt.Errorf("%s and %s both describe %+v",
				other, path, KeyOf(f))
		}
		files[KeyOf(f)] = path

		frags = append(frags, f)
	}

	sort.Slice(frags, func(i, j int) bool {
		a, b := KeyOf(frags[i]), KeyOf(frags[j])
		if a.Runtime != b.Runtime {
			return a.Runtime < b.Runtime
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Handler < b.Handler
	})

	return frags, nil
}
