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

package validator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/log"
	yaml "gopkg.in/yaml.v3"
)

type DefaultValidatorConfig struct {
	// Enable the default validator plugin.
	Enable bool `yaml:"enable" toml:"enable"`
	*ValidatorConfig
	// Overrides provide per-identity overridet to the default configuration.
	Overrides map[string]*ValidatorConfig `yaml:"overrides" toml:"overrides"`
	// RequiredPlugins list globally required plugins. These must be present
	// or otherwise validation will fail.
	// WARNING: This is a global setting and will affect all containers. In
	// particular, if you configure any globally required plugins, you should
	// annotate your static pods to tolerate missing plugins. Failing to do
	// so will prevent static pods from starting.
	// Notes:
	//   Containers can be annotated to tolerate missing plugins using the
	//   toleration annotation, if one is set.
	RequiredPlugins []string `yaml:"requiredPlugins" toml:"required_plugins"`
	// TolerateMissingPlugins is an optional annotation key. If set, it can
	// be used to annotate containers to tolerate missing required plugins.
	TolerateMissingAnnotation string `yaml:"tolerateMissingPluginsAnnotation" toml:"tolerate_missing_plugins_annotation"`
}

// ValidatorConfig provides validation defaults or per identity overrides.
type ValidatorConfig struct {
	// RejectOCIHooks rejects OCI hook injection.
	RejectOCIHooks *bool `yaml:"rejectOCIHooks" toml:"reject_oci_hooks"`
}

// DefaultValidator implements default validation.
type DefaultValidator struct {
	cfg DefaultValidatorConfig
}

const (
	// RequiredPlugins is the annotation key for extra required plugins.
	RequiredPlugins = api.RequiredPluginsAnnotation
)

var (
	// ErrValidation is returned if validation rejects an adjustment.
	ErrValidation = errors.New("validation error")
)

// NewDefaultValidator creates a new instance of the validator.
func NewDefaultValidator(cfg *DefaultValidatorConfig) *DefaultValidator {
	return &DefaultValidator{cfg: *cfg}
}

// SetConfig sets new configuration for the validator.
func (v *DefaultValidator) SetConfig(cfg *DefaultValidatorConfig) {
	if cfg == nil {
		return
	}
	v.cfg = *cfg
}

// ValidateContainerAdjustment validates a container adjustment.
func (v *DefaultValidator) ValidateContainerAdjustment(ctx context.Context, req *api.ValidateContainerAdjustmentRequest) error {
	log.Debugf(ctx, "Validating adjustment of container %s/%s/%s",
		req.GetPod().GetNamespace(), req.GetPod().GetName(), req.GetContainer().GetName())

	plugins := req.GetPluginMap()

	if err := v.validateOCIHooks(req, plugins); err != nil {
		log.Errorf(ctx, "rejecting adjustment: %v", err)
		return err
	}

	if err := v.validateRequiredPlugins(req, plugins); err != nil {
		log.Errorf(ctx, "rejecting adjustment: %v", err)
		return err
	}

	return nil
}

func (v *DefaultValidator) validateOCIHooks(req *api.ValidateContainerAdjustmentRequest, plugins map[string]*api.PluginInstance) error {
	if req.Adjust == nil {
		return nil
	}

	owners, claimed := req.Owners.HooksOwner(req.Container.Id)
	if !claimed {
		return nil
	}

	defaults := v.cfg.ValidatorConfig
	rejected := []string{}

	for _, p := range strings.Split(owners, ",") {
		if instance, ok := plugins[p]; ok {
			cfg := v.cfg.GetConfig(instance.GetIdentity())
			if cfg.RejectOCIHookInjection(defaults) {
				rejected = append(rejected, p)
			}
		}
	}

	if len(rejected) == 0 {
		return nil
	}

	offender := ""

	if len(rejected) == 1 {
		offender = fmt.Sprintf("plugin %q", rejected[0])
	} else {
		offender = fmt.Sprintf("plugins %q", strings.Join(rejected, ","))
	}

	return fmt.Errorf("%w: %s attempted restricted OCI hook injection", ErrValidation, offender)
}

func (v *DefaultValidator) validateRequiredPlugins(req *api.ValidateContainerAdjustmentRequest, plugins map[string]*api.PluginInstance) error {
	var (
		container = req.GetContainer().GetName()
		required  = slices.Clone(v.cfg.RequiredPlugins)
	)

	if tolerateMissing := v.cfg.TolerateMissingAnnotation; tolerateMissing != "" {
		value, ok := req.GetPod().GetEffectiveAnnotation(tolerateMissing, container)
		if ok {
			tolerate, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid %s annotation %q: %w", tolerateMissing, value, err)
			}
			if tolerate {
				return nil
			}
		}
	}

	if value, ok := req.GetPod().GetEffectiveAnnotation(RequiredPlugins, container); ok {
		var annotated []string
		if err := yaml.Unmarshal([]byte(value), &annotated); err != nil {
			return fmt.Errorf("invalid %s annotation %q: %w", RequiredPlugins, value, err)
		}
		required = append(required, annotated...)
	}

	if len(required) == 0 {
		return nil
	}

	absent := []string{}

	for _, r := range required {
		if _, ok := plugins[r]; !ok {
			absent = append(absent, r)
		}
	}

	if len(absent) == 0 {
		return nil
	}

	missing := ""

	if len(absent) == 1 {
		missing = fmt.Sprintf("required plugin %q", absent[0])
	} else {
		missing = fmt.Sprintf("required plugins %q", strings.Join(absent, ","))
	}

	return fmt.Errorf("%w: %s not present", ErrValidation, missing)
}

// GetConfig returns overrides for the named identity if it exists in the
// configuration.
func (cfg *DefaultValidatorConfig) GetConfig(id string) *ValidatorConfig {
	if cfg == nil || cfg.Overrides == nil {
		return nil
	}
	return cfg.Overrides[id]
}

// RejectOCIHookInjection check whether OCI hook injection is rejected,
// falling back to a default configuration if cfg is nil or omits hook
// injection configuration.
func (cfg *ValidatorConfig) RejectOCIHookInjection(defaults *ValidatorConfig) bool {
	if cfg != nil && cfg.RejectOCIHooks != nil {
		return *cfg.RejectOCIHooks
	}

	return defaults != nil && defaults.RejectOCIHookInjection(nil)
}
