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

	"github.com/containerd/nri/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pluginType struct {
	wasmImpl       api.Plugin
	ttrpcImpl      api.PluginService
	useStateChange bool
}

func (p *pluginType) isWasm() bool {
	return p.wasmImpl != nil
}

func (p *pluginType) isTtrpc() bool {
	return p.ttrpcImpl != nil
}

func (p *pluginType) Synchronize(ctx context.Context, req *SynchronizeRequest) (*SynchronizeResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.Synchronize(ctx, req)
	}
	return p.ttrpcImpl.Synchronize(ctx, req)
}

func (p *pluginType) Configure(ctx context.Context, req *ConfigureRequest) (*ConfigureResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.Configure(ctx, req)
	}
	return p.ttrpcImpl.Configure(ctx, req)
}

func (p *pluginType) RunPodSandbox(ctx context.Context, req *RunPodSandboxRequest) (*RunPodSandboxResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.RunPodSandbox(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.RunPodSandbox(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	p.useStateChange = true
	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event: Event_RUN_POD_SANDBOX,
		Pod:   req.Pod,
	})

	return &RunPodSandboxResponse{}, err
}

func (p *pluginType) StopPodSandbox(ctx context.Context, req *StopPodSandboxRequest) (*StopPodSandboxResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.StopPodSandbox(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.StopPodSandbox(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event: Event_STOP_POD_SANDBOX,
		Pod:   req.Pod,
	})

	return &StopPodSandboxResponse{}, err
}

func (p *pluginType) RemovePodSandbox(ctx context.Context, req *RemovePodSandboxRequest) (*RemovePodSandboxResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.RemovePodSandbox(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.RemovePodSandbox(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event: Event_REMOVE_POD_SANDBOX,
		Pod:   req.Pod,
	})

	return &RemovePodSandboxResponse{}, err
}

func (p *pluginType) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*CreateContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.CreateContainer(ctx, req)
	}
	return p.ttrpcImpl.CreateContainer(ctx, req)
}

func (p *pluginType) PostCreateContainer(ctx context.Context, req *PostCreateContainerRequest) (*PostCreateContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.PostCreateContainer(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.PostCreateContainer(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event:     Event_POST_CREATE_CONTAINER,
		Pod:       req.Pod,
		Container: req.Container,
	})

	return &PostCreateContainerResponse{}, err
}

func (p *pluginType) StartContainer(ctx context.Context, req *StartContainerRequest) (*StartContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.StartContainer(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.StartContainer(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event:     Event_START_CONTAINER,
		Pod:       req.Pod,
		Container: req.Container,
	})

	return &StartContainerResponse{}, err
}

func (p *pluginType) PostStartContainer(ctx context.Context, req *PostStartContainerRequest) (*PostStartContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.PostStartContainer(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.PostStartContainer(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event:     Event_POST_START_CONTAINER,
		Pod:       req.Pod,
		Container: req.Container,
	})

	return &PostStartContainerResponse{}, err
}

func (p *pluginType) UpdateContainer(ctx context.Context, req *UpdateContainerRequest) (*UpdateContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.UpdateContainer(ctx, req)
	}
	return p.ttrpcImpl.UpdateContainer(ctx, req)
}

func (p *pluginType) PostUpdateContainer(ctx context.Context, req *PostUpdateContainerRequest) (*PostUpdateContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.PostUpdateContainer(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.PostUpdateContainer(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event:     Event_POST_UPDATE_CONTAINER,
		Pod:       req.Pod,
		Container: req.Container,
	})

	return &PostUpdateContainerResponse{}, err
}

func (p *pluginType) StopContainer(ctx context.Context, req *StopContainerRequest) (*StopContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.StopContainer(ctx, req)
	}
	return p.ttrpcImpl.StopContainer(ctx, req)
}

func (p *pluginType) RemoveContainer(ctx context.Context, req *RemoveContainerRequest) (*RemoveContainerResponse, error) {
	if p.wasmImpl != nil {
		return p.wasmImpl.RemoveContainer(ctx, req)
	}

	if !p.useStateChange {
		rpl, err := p.ttrpcImpl.RemoveContainer(ctx, req)
		if err == nil || status.Code(err) != codes.Unimplemented {
			return rpl, err
		}
		p.useStateChange = true
	}

	_, err := p.ttrpcImpl.StateChange(ctx, &StateChangeEvent{
		Event:     Event_REMOVE_CONTAINER,
		Pod:       req.Pod,
		Container: req.Container,
	})

	return &RemoveContainerResponse{}, err
}

func (p *pluginType) StateChange(ctx context.Context, req *StateChangeEvent) (err error) {
	if p.wasmImpl != nil {
		_, err = p.wasmImpl.StateChange(ctx, req)
	} else {
		_, err = p.ttrpcImpl.StateChange(ctx, req)
	}
	return err
}
