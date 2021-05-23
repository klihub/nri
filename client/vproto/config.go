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

package vproto

import (
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
)

const (
	// ExpectedVersion is the configuration version we expect.
	ExpectedVersion = "vproto"
	// pluginConfigSubdir is the modular config directory for plugins
	pluginConfigSubdir = "conf.d"
)

// Config represents the runtime configuration for NRI.
type Config struct {
	// Version of NRI this configuration applies to.
	Version string `json:"version,omitempty"`
	// DisableDynamicPlugins disables connections from dynamic plugins.
	DisableDynamicPlugins bool `json:"disableDynamicPlugins"`
	// EnablePlugins, if given, enables plugins matching any glob patterns.
	EnablePlugins []string `json:"enablePlugins"`
	// DisablePlugins disables any plugin matching any of the glob patters.
	DisablePlugins []string `json:"disablePlugins"`
	// path to the configuration file.
	path string
}

// Read the configuration from a file.
func (cfg *Config) read(path string) error {
	buf, err := ioutil.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return errors.Wrapf(err, "failed to read configuration %s", path)
	}

	config := Config{path: path}
	err = yaml.Unmarshal(buf, &config)
	if err != nil {
		return errors.Wrapf(err, "failed to parse configuration %s", path)
	}

	if config.Version != "" && config.Version != ExpectedVersion {
		return errors.Errorf("configuration version `%s`, expected `%s`",
			config.Version, ExpectedVersion)
	}

	*cfg = config

	return nil
}

// isPluginEnabled returns true if a plugin is enabled.
func (cfg *Config) isPluginEnabled(name string) bool {
	enabled, explicit := cfg.enabledPlugin(name)
	if enabled && explicit {
		return true
	}
	return enabled && !cfg.disabledPlugin(name)
}

// enabledPlugin checks if a plugin is enabled and if this is explicit.
func (cfg *Config) enabledPlugin(name string) (enabled, explicit bool) {
	if len(cfg.EnablePlugins) == 0 {
		return true, false
	}

	for _, pattern := range cfg.EnablePlugins {
		if pattern == name {
			return true, true
		}
		match, err := filepath.Match(pattern, name)
		if err != nil {
			log.Errorf("NRI: invalid plugin pattern `%s` in %s: %v",
				pattern, cfg.path, err)
		}
		if match {
			return true, false
		}
	}

	return false, false
}

// disabledPlugin checks if a plugin is disabled.
func (cfg *Config) disabledPlugin(name string) bool {
	for _, pattern := range cfg.DisablePlugins {
		if pattern == name {
			return true
		}
		match, err := filepath.Match(pattern, name)
		if err != nil {
			log.Errorf("NRI: invalid plugin pattern `%s` in %s: %v",
				pattern, cfg.path, err)
		}
		if match {
			return true
		}
	}

	return false
}

// getPluginConfig reads configuration for the given plugin.
func (cfg *Config) getPluginConfig(name string) (string, error) {
	dir := filepath.Join(filepath.Dir(cfg.path), pluginConfigSubdir)
	buf, err := ioutil.ReadFile(filepath.Join(dir, name) + ".conf")
	if err != nil && os.IsNotExist(err) {
		return "", nil
	}

	return string(buf), err
}
