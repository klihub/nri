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
	"os"
	"io"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"
)

// Reload the configuration from the given path (if it has changed).
func (c *Config) Reload(path string, searchPath ...string) error {
	if c.Version == "" {
		c.Version = Version
	}

	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return errors.Wrapf(err, "failed to open NRI config %q", path)
		}
		c.Plugins = nil
		c.info = nil
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return errors.Wrapf(err, "failed to stat NRI config %q", path)
	}
	if c.info != nil {
		if os.SameFile(*c.info, info) &&
			(*c.info).ModTime() == info.ModTime() {
			return nil
		}
	}

	buf := make([]byte, info.Size(), info.Size())
	_, err = f.Read(buf)
	if err != nil && err != io.EOF {
		return errors.Wrapf(err, "failed to read NRI config %q", path)
	}

	conf := Config{}
	err = yaml.Unmarshal(buf, &conf)
	if err != nil {
		return errors.Wrapf(err, "failed to parse NRI config %q", path)
	}

	c.Version = conf.Version
	c.Plugins = conf.Plugins
	c.info = &info

	for _, plugin := range c.Plugins {
		plugin.Resolve(searchPath...)
	}

	return nil
}
