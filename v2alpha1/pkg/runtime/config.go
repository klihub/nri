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

package runtime

import (
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
)

const (
	// PluginConfigSubdir is the drop-in directory for plugin configuration.
	PluginConfigSubdir = "conf.d"
)

// Config is the runtime configuration for NRI.
type Config struct {
	// DisablePluginConnections disables runtime connections from plugins.
	DisablePluginConnections bool `json:"disablePluginConnections"`
	// EnablePlugins enables matching plugins by name or glob pattern.
	EnablePlugins []string `json:"enablePlugins"`
	// DisablePlugins disables matching plugins by name or glob pattern.
	DisablePlugins []string `json:"disablePlugins"`

	path   string
	dropIn string
}

// DefaultConfig returns the default NRI configuration for a given path.
// This configuration should be identical to what ReadConfig would return
// for an empty file at the given location. If the given path is empty,
// DefaultConfigPath is used instead.
func DefaultConfig(path string) *Config {
	if path == "" {
		path = DefaultConfigPath
	}
	return &Config{
		path:   path,
		dropIn: filepath.Join(filepath.Dir(path), PluginConfigSubdir),
	}
}

// ReadConfig reads the NRI runtime configuration from a file.
func ReadConfig(path string) (*Config, error) {
	buf, err := ioutil.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, err
	}
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read file %q", path)
	}

	cfg := &Config{}
	err = yaml.UnmarshalStrict(buf, cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse file %q", path)
	}
	cfg.path = path

	if err = cfg.validate(); err != nil {
		return nil, errors.Wrapf(err, "invalid configuration file %q", path)
	}

	cfg.dropIn = filepath.Join(filepath.Dir(path), PluginConfigSubdir)

	return cfg, nil
}

func (cfg *Config) validate() error {
	for _, pattern := range cfg.EnablePlugins {
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return errors.Wrapf(err, "invalid pattern '%s'", pattern)
		}
	}
	for _, pattern := range cfg.DisablePlugins {
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return errors.Wrapf(err, "invalid pattern '%s'", pattern)
		}
	}
	return nil
}

func (cfg *Config) isPluginEnabled(id, base string) bool {
	enabled, explicit := cfg.enabled(id, base)
	if explicit {
		return true
	}
	if enabled {
		return !cfg.disabled(id, base)
	}
	return false
}

func (cfg *Config) enabled(id, base string) (enabled, explicit bool) {
	if len(cfg.EnablePlugins) == 0 {
		return true, false
	}

	name := ""
	if id != "" {
		name = id + "-" + base
	}

	for _, pattern := range cfg.EnablePlugins {
		if name != "" {
			if pattern == name {
				return true, true
			}
			if match, _ := filepath.Match(pattern, name); match {
				return true, false
			}
		}
		if pattern == base {
			return true, false
		}
		if match, _ := filepath.Match(pattern, base); match {
			return true, false
		}
	}

	return false, false
}

func (cfg *Config) disabled(id, base string) bool {
	name := ""
	if id != "" {
		name = id + "-" + base
	}

	for _, pattern := range cfg.DisablePlugins {
		if name != "" {
			if pattern == name {
				return true
			}
			if match, _ := filepath.Match(pattern, name); match {
				return true
			}
		}
		if pattern == base {
			return true
		}
		if match, _ := filepath.Match(pattern, base); match {
			return true
		}
	}

	return false
}

func (cfg *Config) getPluginConfig(id, base string) (string, error) {
	name := id + "-" + base
	dropIns := []string{
		filepath.Join(cfg.dropIn, name+".conf"),
		filepath.Join(cfg.dropIn, base+".conf"),
	}

	for _, path := range dropIns {
		buf, err := ioutil.ReadFile(path)
		if err == nil {
			return string(buf), nil
		}
		if !os.IsNotExist(err) {
			return "", errors.Wrapf(err,
				"failed to read configuration for plugin %q", name)
		}
	}

	return "", nil
}
