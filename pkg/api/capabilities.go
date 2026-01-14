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
	"slices"
	"strings"
)

// CapabilityMask corresponds to a set of enumerated Capabilities.
type CapabilityMask []uint64

const (
	capabilityWords = (int(Capability_MAX_CAPABILITY) + 63) / 64
)

// NewCapabilityMask creates a CapabilityMask from a list of Capabilities.
func NewCapabilityMask(capabilities ...Capability) CapabilityMask {
	mask := make(CapabilityMask, capabilityWords, capabilityWords)
	for _, c := range capabilities {
		if c < 0 || c >= Capability_MAX_CAPABILITY {
			continue
		}
		idx := c / 64
		bit := uint64(1) << uint(c%64)
		mask[idx] |= bit
	}
	return mask
}

// Clone returns a copy of the capability mask.
func (m CapabilityMask) Clone() CapabilityMask {
	return slices.Clone(m)
}

// Set returns a new mask with the given extra capabilities set.
func (m CapabilityMask) Set(capabilities ...Capability) CapabilityMask {
	n := make(CapabilityMask, capabilityWords, capabilityWords)
	copy(n, m)
	for _, c := range capabilities {
		if c < 0 || c >= Capability_MAX_CAPABILITY {
			continue
		}
		idx := c / 64
		bit := uint64(1) << uint(c%64)
		m[idx] |= bit
	}
	return n
}

// Clear returns a new mask with the given extra capabilities cleared.
func (m CapabilityMask) Clear(capabilities ...Capability) CapabilityMask {
	n := make(CapabilityMask, capabilityWords, capabilityWords)
	copy(n, m)
	for _, c := range capabilities {
		if c < 0 || c >= Capability_MAX_CAPABILITY {
			continue
		}
		idx := c / 64
		bit := uint64(1) << uint(c%64)
		m[idx] &^= bit
	}
	return n
}

// IsSet checks if the given capabilities are set in the mask.
func (m CapabilityMask) IsSet(capabilties ...Capability) bool {
	for _, c := range capabilties {
		if c < 0 || c >= Capability_MAX_CAPABILITY {
			continue
		}
		idx := c / 64
		bit := uint64(1) << uint(c%64)
		if (m[idx] & bit) == 0 {
			return false
		}
	}
	return true
}

// IsSubsetOf checks if the given mask is a subset of this mask.
func (m CapabilityMask) IsSubsetOf(o CapabilityMask) bool {
	for i, w := range m {
		if (o[i] & w) != w {
			return false
		}
	}
	return true
}

// Difference returns the capabilities in this mask that are not in the other mask.
func (m CapabilityMask) Difference(o CapabilityMask) CapabilityMask {
	n := make(CapabilityMask, capabilityWords, capabilityWords)
	for i, w := range m {
		n[i] = w &^ o[i]
	}
	return n
}

// IsEmpty checks if the mask is empty.
func (m CapabilityMask) IsEmpty() bool {
	for _, w := range m {
		if w != 0 {
			return false
		}
	}
	return true
}

// String returns the capabilities present in the mask as a comma-separated string.
func (m CapabilityMask) String() string {
	str := strings.Builder{}
	for c := Capability(0); c < Capability_MAX_CAPABILITY; c++ {
		if m.IsSet(c) {
			if str.Len() > 0 {
				str.WriteString(",")
			}
			str.WriteString(Capability_name[int32(c)])
		}
	}
	return str.String()
}
