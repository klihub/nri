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

package cri

import (
	"context"
	"errors"
	"fmt"

	internalapi "k8s.io/cri-api/pkg/apis"
	remote "k8s.io/cri-client/pkg"

	"github.com/containerd/nri/test/nri-test/framework"
)

// Client holds connections to the CRI runtime and image services.
type Client struct {
	// Runtime is the CRI runtime service.
	Runtime internalapi.RuntimeService
	// Image is the CRI image service.
	Image internalapi.ImageManagerService
}

// NewClient connects to the CRI services named in cfg.
func NewClient(ctx context.Context, cfg *framework.Config) (*Client, error) {
	runtime, err := remote.NewRemoteRuntimeServiceBuilder().
		WithEndpoint(cfg.RuntimeEndpoint).
		WithConnectionTimeout(cfg.Timeout).
		Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to CRI runtime service at %s: %w",
			cfg.RuntimeEndpoint, err)
	}

	image, err := remote.NewRemoteImageServiceBuilder().
		WithEndpoint(cfg.ImageEndpoint).
		WithConnectionTimeout(cfg.Timeout).
		Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to CRI image service at %s: %w",
			cfg.ImageEndpoint, err)
	}

	return &Client{Runtime: runtime, Image: image}, nil
}

// Close releases the client's connections.
func (c *Client) Close(ctx context.Context) error {
	var errs []error
	if c.Runtime != nil {
		errs = append(errs, c.Runtime.Close(ctx))
	}
	if c.Image != nil {
		errs = append(errs, c.Image.Close(ctx))
	}

	return errors.Join(errs...)
}
