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

package adaptation

import (
	"context"

	old "github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/api/convert"
	api "github.com/containerd/nri/pkg/api/v1beta1"
	"github.com/containerd/nri/pkg/log"
)

type v1alpha1Runtime struct {
	p        *plugin
	v1alpha1 *v1alpha1Plugin
}

func (p *plugin) RegisterV1Alpha1RuntimeService() error {
	v1 := &v1alpha1Runtime{p: p}
	old.RegisterRuntimeService(p.rpcs, v1)
	return nil
}

func (v1 *v1alpha1Runtime) RegisterPlugin(ctx context.Context, req *old.RegisterPluginRequest) (*old.Empty, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	v1.v1alpha1 = &v1alpha1Plugin{r: v1}
	v1.v1alpha1.api = old.NewPluginClient(v1.p.rpcc)

	v1.p.impl.ttrpcImpl = v1.v1alpha1

	nreq := convert.RegisterPluginRequest(req)
	nrpl, err := v1.p.RegisterPlugin(ctx, nreq)
	if err != nil {
		return nil, err
	}

	return convert.RegisterPluginResponse(nrpl), nil
}

func (v1 *v1alpha1Runtime) UpdateContainers(ctx context.Context, req *old.UpdateContainersRequest) (*old.UpdateContainersResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	nreq := convert.UpdateContainersRequest(req)

	nrpl, err := v1.p.UpdateContainers(ctx, nreq)
	if err != nil {
		return nil, err
	}

	return convert.UpdateContainersResponse(nrpl), nil
}

type v1alpha1Plugin struct {
	r   *v1alpha1Runtime
	api old.PluginService
}

func (p *v1alpha1Plugin) Configure(ctx context.Context, req *api.ConfigureRequest) (*api.ConfigureResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.ConfigureRequest(req)

	orpl, err := p.api.Configure(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.ConfigureResponse(orpl), nil
}

func (p *v1alpha1Plugin) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	oreq := convert.SynchronizeRequest(req)

	orpl, err := p.api.Synchronize(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.SynchronizeResponse(orpl), nil
}

func (p *v1alpha1Plugin) Shutdown(ctx context.Context, req *api.ShutdownRequest) (*api.ShutdownResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	return &api.ShutdownResponse{}, nil
}

func (p *v1alpha1Plugin) RunPodSandbox(ctx context.Context, req *api.RunPodSandboxRequest) (*api.RunPodSandboxResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.RunPodSandboxRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.RunPodSandboxResponse(orpl), nil
}

func (p *v1alpha1Plugin) UpdatePodSandbox(ctx context.Context, req *api.UpdatePodSandboxRequest) (*api.UpdatePodSandboxResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.UpdatePodSandboxRequest(req)

	orpl, err := p.api.UpdatePodSandbox(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.UpdatePodSandboxResponse(orpl), nil
}

func (p *v1alpha1Plugin) PostUpdatePodSandbox(ctx context.Context, req *api.PostUpdatePodSandboxRequest) (*api.PostUpdatePodSandboxResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.PostUpdatePodSandboxRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.PostUpdatePodSandboxResponse(orpl), nil
}

func (p *v1alpha1Plugin) StopPodSandbox(ctx context.Context, req *api.StopPodSandboxRequest) (*api.StopPodSandboxResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.StopPodSandboxRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.StopPodSandboxResponse(orpl), nil
}

func (p *v1alpha1Plugin) RemovePodSandbox(ctx context.Context, req *api.RemovePodSandboxRequest) (*api.RemovePodSandboxResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.RemovePodSandboxRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.RemovePodSandboxResponse(orpl), nil
}

func (p *v1alpha1Plugin) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.CreateContainerRequest(req)

	orpl, err := p.api.CreateContainer(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.CreateContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) PostCreateContainer(ctx context.Context, req *api.PostCreateContainerRequest) (*api.PostCreateContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.PostCreateContainerRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.PostCreateContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) StartContainer(ctx context.Context, req *api.StartContainerRequest) (*api.StartContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.StartContainerRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.StartContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) PostStartContainer(ctx context.Context, req *api.PostStartContainerRequest) (*api.PostStartContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.PostStartContainerRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.PostStartContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.UpdateContainerRequest(req)

	orpl, err := p.api.UpdateContainer(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.UpdateContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) PostUpdateContainer(ctx context.Context, req *api.PostUpdateContainerRequest) (*api.PostUpdateContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.PostUpdateContainerRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.PostUpdateContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.StopContainerRequest(req)

	orpl, err := p.api.StopContainer(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.StopContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) RemoveContainer(ctx context.Context, req *api.RemoveContainerRequest) (*api.RemoveContainerResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.RemoveContainerRequest(req)

	orpl, err := p.api.StateChange(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.RemoveContainerResponse(orpl), nil
}

func (p *v1alpha1Plugin) ValidateContainerAdjustment(ctx context.Context, req *api.ValidateContainerAdjustmentRequest) (*api.ValidateContainerAdjustmentResponse, error) {
	log.Infof(ctx, "v1alpha1 %T: %v", req, req)

	oreq := convert.ValidateContainerAdjustmentRequest(req)

	orpl, err := p.api.ValidateContainerAdjustment(ctx, oreq)
	if err != nil {
		return nil, err
	}

	return convert.ValidateContainerAdjustmentResponse(orpl), nil
}
