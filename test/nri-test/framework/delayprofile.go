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
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"

	"sigs.k8s.io/yaml"
)

// allPlugins is the mapping key standing for the simulated plugins a delay
// file does not name explicitly.
const allPlugins = "*"

// DelaySource is one entry of delay configuration: either an inline
// -nri-test.scale-delay spec or the path of a -nri-test.scale-delay-file.
//
// Both flags append into one list, in the order they appeared on the command
// line, because the two have to compose: a file can set a baseline that a
// later inline item refines, or the other way round. Keeping them in one
// ordered list is what makes "later wins" a single rule covering both rather
// than a precedence table between the flags.
type DelaySource struct {
	// Spec is an inline -nri-test.scale-delay entry.
	Spec string
	// Path is a -nri-test.scale-delay-file entry.
	Path string
}

// DelayProfile is one named set of simulated processing times, keyed by NRI
// hook name, each a duration string like "50ms".
//
// An event the profile does not name is not delayed by it. A profile therefore
// states what a plugin spends time on and says nothing about the rest, which
// is what lets a later profile refine an earlier one instead of erasing it.
// Durations stay strings so that the file and the flag parse them the same
// way, and so that a bare number cannot quietly mean nanoseconds.
type DelayProfile map[string]string

// delayFile is the on-disk form of a -nri-test.scale-delay-file: named
// profiles, and which plugin simulates which of them.
type delayFile struct {
	// Profiles are the profiles the mapping selects from. One defined and
	// never named is not a mistake -- a file may hold a library of them.
	Profiles map[string]DelayProfile `json:"profiles"`
	// Plugins maps a plugin index, or allPlugins for the rest of them, to the
	// name of the profile that plugin simulates.
	Plugins map[string]string `json:"plugins"`
}

// addDelayFile reads one delay-profile file and records the delays it asks
// for, as the same rules an inline spec produces, so that the two flags
// compose by position and need no precedence rule of their own.
func (p *ScalePlan) addDelayFile(path string) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var f delayFile

	// Strictly: a section name that is almost right -- `plugin:` for
	// `plugins:` -- has to fail loudly rather than be dropped. Silent
	// acceptance is the failure this measurement cannot afford, because the
	// run still produces numbers and nothing in them says the file was
	// ignored.
	if err := yaml.UnmarshalStrict(blob, &f); err != nil {
		return err
	}

	if len(f.Plugins) == 0 {
		return fmt.Errorf("no plugins mapping: a file whose plugins section is " +
			"missing or empty delays nothing, which is refused rather than ignored")
	}

	named, rest, err := p.readDelayMapping(f.Plugins)
	if err != nil {
		return err
	}

	// Explicit entries first and the rest after, which is what makes "*" mean
	// the plugins the mapping did not name. Ordering the two by where their
	// keys sit in the file is not available: YAML mappings have no order.
	for _, idx := range slices.Sorted(maps.Keys(named)) {
		if err := p.applyDelayProfile(idx, named[idx], f.Profiles); err != nil {
			return err
		}
	}

	if rest == "" {
		return nil
	}

	if p.Plugins <= p.firstSimulated() {
		return fmt.Errorf("%q covers the simulated plugins and there are no simulated "+
			"plugins to cover: -nri-test.scale-plugins is %d, so the only plugin is "+
			"the recorder, which must not be delayed", allPlugins, p.Plugins)
	}

	for idx := p.firstSimulated(); idx < p.Plugins; idx++ {
		if _, ok := named[idx]; ok {
			continue
		}

		if err := p.applyDelayProfile(idx, rest, f.Profiles); err != nil {
			return err
		}
	}

	return nil
}

// readDelayMapping splits a plugins section into the explicitly named indexes
// and the profile standing for the rest, refusing a key that is neither.
func (p *ScalePlan) readDelayMapping(m map[string]string) (map[int]string, string, error) {
	named, rest := map[int]string{}, ""

	for key, profile := range m {
		if key == allPlugins {
			rest = profile

			continue
		}

		idx, err := strconv.Atoi(key)
		if err != nil {
			return nil, "", fmt.Errorf("bad plugins key %q: want a plugin index or %q",
				key, allPlugins)
		}

		if idx < 0 || idx >= p.Plugins {
			return nil, "", fmt.Errorf("plugin %d does not exist; indexes run 0..%d",
				idx, p.Plugins-1)
		}

		named[idx] = profile
	}

	return named, rest, nil
}

// applyDelayProfile turns one plugin's profile into delay rules and records
// the profile's name against that plugin, for Describe to report.
func (p *ScalePlan) applyDelayProfile(idx int, name string, profiles map[string]DelayProfile) error {
	profile, ok := profiles[name]
	if !ok {
		return fmt.Errorf("plugin %d names profile %q, which this file does not define",
			idx, name)
	}

	for _, event := range slices.Sorted(maps.Keys(profile)) {
		ev, err := parseSimulatable(event)
		if err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}

		d, err := parseDelay(profile[event])
		if err != nil {
			return fmt.Errorf("profile %q %s: %w", name, event, err)
		}

		if err := p.addRule(delayRule{plugin: idx, event: ev, delay: d}); err != nil {
			return err
		}
	}

	p.profiles[idx] = name

	return nil
}
