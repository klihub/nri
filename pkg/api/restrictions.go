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

import fmt "fmt"

var (
	RestrictionError        = fmt.Errorf("restriction violation")
	OciHooksRestricted      = fmt.Errorf("%w: OCI hook adjustment is not allowed", RestrictionError)
	NamespacesRestricted    = fmt.Errorf("%w: namespace adjustment is not allowed", RestrictionError)
	SeccompPolicyRestricted = fmt.Errorf("%w: seccpomp adjustment is not allowed", RestrictionError)
)

func (r *Restrictions) CheckAdjustment(a *ContainerAdjustment) error {
	if a == nil || r == nil {
		return nil
	}
	if err := r.checkOciHooks(a); err != nil {
		return err
	}
	if err := r.checkNamespaces(a); err != nil {
		return err
	}
	if err := r.checkSeccompPolicy(a); err != nil {
		return err
	}
	return nil
}

func (r *Restrictions) checkOciHooks(a *ContainerAdjustment) error {
	if !r.OciHooks || a.Hooks == nil {
		return nil
	}

	switch {
	case a.Hooks.Prestart != nil:
		return OciHooksRestricted
	case a.Hooks.CreateRuntime != nil:
		return OciHooksRestricted
	case a.Hooks.CreateContainer != nil:
		return OciHooksRestricted
	case a.Hooks.StartContainer != nil:
		return OciHooksRestricted
	case a.Hooks.Poststart != nil:
		return OciHooksRestricted
	case a.Hooks.Poststop != nil:
		return OciHooksRestricted
	}

	return nil
}

func (r *Restrictions) checkNamespaces(a *ContainerAdjustment) error {
	if !r.Namespaces || a.Linux == nil || len(a.Linux.Namespaces) == 0 {
		return nil
	}

	return NamespacesRestricted
}

func (r *Restrictions) checkSeccompPolicy(a *ContainerAdjustment) error {
	if !r.SeccompPolicy || a.Linux == nil || a.Linux.SeccompPolicy == nil {
		return nil
	}

	return SeccompPolicyRestricted
}

func (r *Restrictions) AllowOciHooks() bool {
	return r == nil || !r.OciHooks
}

func (r *Restrictions) AllowNamespaces() bool {
	return r == nil || !r.Namespaces
}

func (r *Restrictions) AllowSeccompPolicy() bool {
	return r == nil || !r.SeccompPolicy
}
