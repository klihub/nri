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
	"github.com/containerd/nri/pkg/plugin"
	yaml "gopkg.in/yaml.v3"
)

type DefaultValidatorConfig struct {
	// Enable the default validator plugin.
	Enable bool `yaml:"enable" toml:"enable"`
	// RejectOCIHooks fails validation if any plugin injects OCI hooks.
	RejectOCIHooks bool `yaml:"rejectOCIHooks" toml:"reject_oci_hooks"`
	// RejectNamespaceAdjustment fails validation if any plugin adjusts Linux namespaces.
	RejectNamespaces bool `yaml:"rejectNamespaces" toml:"reject_namespaces"`
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

// DefaultValidator implements default validation.
type DefaultValidator struct {
	cfg DefaultValidatorConfig
}

const (
	// RequiredPlugins is the annotation key for extra required plugins.
	RequiredPlugins = plugin.RequiredPluginsAnnotation
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

	if err := v.validateOCIHooks(req); err != nil {
		log.Errorf(ctx, "rejecting adjustment: %v", err)
		return err
	}

	if err := v.validateNamespaces(req); err != nil {
		log.Errorf(ctx, "rejecting adjustment: %v", err)
		return err
	}

	if err := v.validateRequiredPlugins(req); err != nil {
		log.Errorf(ctx, "rejecting adjustment: %v", err)
		return err
	}

	return nil
}

func (v *DefaultValidator) validateOCIHooks(req *api.ValidateContainerAdjustmentRequest) error {
	if req.Adjust == nil {
		return nil
	}

	if !v.cfg.RejectOCIHooks {
		return nil
	}

	owners, claimed := req.Owners.HooksOwner(req.Container.Id)
	if !claimed {
		return nil
	}

	offender := ""

	if !strings.Contains(owners, ",") {
		offender = fmt.Sprintf("plugin %q", owners)
	} else {
		offender = fmt.Sprintf("plugins %q", owners)
	}

	return fmt.Errorf("%w: %s attempted restricted OCI hook injection", ErrValidation, offender)
}

func (v *DefaultValidator) validateNamespaces(req *api.ValidateContainerAdjustmentRequest) error {
	if req.Adjust == nil {
		return nil
	}

	if !v.cfg.RejectNamespaces {
		return nil
	}

	owners, claimed := req.Owners.NamespaceOwners(req.Container.Id)
	if !claimed {
		return nil
	}

	offenders := ""
	sep := ""

	if len(owners) > 1 {
		offenders = "plugins "
	} else {
		offenders = "plugin "
	}
	for ns, plugin := range owners {
		offenders += sep + fmt.Sprintf("%q (namespace %q)", plugin, ns)
		sep = ", "
	}

	return fmt.Errorf("%w: attempted restricted namespace adjustment by %s",
		ErrValidation, offenders)
}

func (v *DefaultValidator) validateRequiredPlugins(req *api.ValidateContainerAdjustmentRequest) error {
	var (
		container = req.GetContainer().GetName()
		required  = slices.Clone(v.cfg.RequiredPlugins)
	)

	if tolerateMissing := v.cfg.TolerateMissingAnnotation; tolerateMissing != "" {
		value, ok := plugin.GetEffectiveAnnotation(req.GetPod(), tolerateMissing, container)
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

	if value, ok := plugin.GetEffectiveAnnotation(req.GetPod(), RequiredPlugins, container); ok {
		var annotated []string
		if err := yaml.Unmarshal([]byte(value), &annotated); err != nil {
			return fmt.Errorf("invalid %s annotation %q: %w", RequiredPlugins, value, err)
		}
		required = append(required, annotated...)
	}

	if len(required) == 0 {
		return nil
	}

	plugins := req.GetPluginMap()
	missing := []string{}

	for _, r := range required {
		if _, ok := plugins[r]; !ok {
			missing = append(missing, r)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	offender := ""

	if len(missing) == 1 {
		offender = fmt.Sprintf("required plugin %q", missing[0])
	} else {
		offender = fmt.Sprintf("required plugins %q", strings.Join(missing, ","))
	}

	return fmt.Errorf("%w: %s not present", ErrValidation, offender)
}
