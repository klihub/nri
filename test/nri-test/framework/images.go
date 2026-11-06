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
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
)

var (
	imagesMu   sync.Mutex
	extraImage []string
)

// UseImage declares that some test creates containers from image, rather than
// from the configured default. Call it from an init function of the file that
// holds the test, so that the declaration cannot drift from the use and the
// image shows up in -nri-test.list-images and -nri-test.pull-images.
//
// A test that names an image in a ContainerSpec without declaring it here still
// works, but its image is neither listed nor pre-pulled, and CRI's
// CreateContainer does not pull.
func UseImage(image string) {
	imagesMu.Lock()
	defer imagesMu.Unlock()

	if image != "" && !slices.Contains(extraImage, image) {
		extraImage = append(extraImage, image)
	}
}

// Images returns every image the suite uses, without duplicates: the configured
// default first, then declared ones in sorted order.
//
// Static, like test listing: it needs no runtime, so it works with an
// incomplete Config.
func Images(cfg *Config) []string {
	def := cfg.Image
	if def == "" {
		def = DefaultImage
	}

	imagesMu.Lock()
	defer imagesMu.Unlock()

	rest := slices.DeleteFunc(slices.Clone(extraImage), func(i string) bool { return i == def })
	slices.Sort(rest)

	return append([]string{def}, rest...)
}

// ListImages prints one image per line.
func ListImages(w io.Writer, cfg *Config) {
	for _, image := range Images(cfg) {
		fmt.Fprintln(w, image)
	}
}

// PullImages pre-pulls every image the suite uses into the node's image store
// and returns, creating nothing else. It is the whole of a
// -nri-test.pull-images run. Each image gets its own timeout, as images may be
// large.
func PullImages(cfg *Config, newDriver DriverFactory) error {
	var (
		d   Driver
		err error
	)

	err = withTimeout(cfg.Timeout, func(ctx context.Context) error {
		d, _, err = newDriver(ctx, cfg)

		return err
	})
	if err != nil {
		return err
	}

	if d == nil {
		return errors.New("driver factory returned no driver and no error")
	}

	defer func() { _ = withTimeout(cfg.Timeout, d.Close) }()

	puller, ok := d.(imagePuller)
	if !ok {
		return fmt.Errorf("driver %s cannot pull images", d.Name())
	}

	var errs []error

	for _, image := range Images(cfg) {
		err := withTimeout(cfg.Timeout, func(ctx context.Context) error {
			return puller.PullImage(ctx, image)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to pull %s: %w", image, err))

			continue
		}

		fmt.Printf("pulled %s\n", image)
	}

	return errors.Join(errs...)
}
