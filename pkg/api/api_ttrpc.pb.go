// Code generated by protoc-gen-go-ttrpc. DO NOT EDIT.
// source: pkg/api/api.proto
package api

import (
	context "context"
	ttrpc "github.com/containerd/ttrpc"
)

type RuntimeService interface {
	RegisterPlugin(context.Context, *RegisterPluginRequest) (*Empty, error)
	UpdateContainers(context.Context, *UpdateContainersRequest) (*UpdateContainersResponse, error)
}

func RegisterRuntimeService(srv *ttrpc.Server, svc RuntimeService) {
	srv.RegisterService("nri.pkg.api.v1alpha1.Runtime", &ttrpc.ServiceDesc{
		Methods: map[string]ttrpc.Method{
			"RegisterPlugin": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req RegisterPluginRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.RegisterPlugin(ctx, &req)
			},
			"UpdateContainers": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req UpdateContainersRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.UpdateContainers(ctx, &req)
			},
		},
	})
}

type runtimeClient struct {
	client *ttrpc.Client
}

func NewRuntimeClient(client *ttrpc.Client) RuntimeService {
	return &runtimeClient{
		client: client,
	}
}

func (c *runtimeClient) RegisterPlugin(ctx context.Context, req *RegisterPluginRequest) (*Empty, error) {
	var resp Empty
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Runtime", "RegisterPlugin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *runtimeClient) UpdateContainers(ctx context.Context, req *UpdateContainersRequest) (*UpdateContainersResponse, error) {
	var resp UpdateContainersResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Runtime", "UpdateContainers", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type PluginService interface {
	Configure(context.Context, *ConfigureRequest) (*ConfigureResponse, error)
	Synchronize(context.Context, *SynchronizeRequest) (*SynchronizeResponse, error)
	Shutdown(context.Context, *Empty) (*Empty, error)
	CreateContainer(context.Context, *CreateContainerRequest) (*CreateContainerResponse, error)
	UpdateContainer(context.Context, *UpdateContainerRequest) (*UpdateContainerResponse, error)
	StopContainer(context.Context, *StopContainerRequest) (*StopContainerResponse, error)
	StateChange(context.Context, *StateChangeEvent) (*Empty, error)
	AdjustPodSandboxNetwork(context.Context, *AdjustPodSandboxNetworkRequest) (*AdjustPodSandboxNetworkResponse, error)
}

func RegisterPluginService(srv *ttrpc.Server, svc PluginService) {
	srv.RegisterService("nri.pkg.api.v1alpha1.Plugin", &ttrpc.ServiceDesc{
		Methods: map[string]ttrpc.Method{
			"Configure": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req ConfigureRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.Configure(ctx, &req)
			},
			"Synchronize": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req SynchronizeRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.Synchronize(ctx, &req)
			},
			"Shutdown": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req Empty
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.Shutdown(ctx, &req)
			},
			"CreateContainer": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req CreateContainerRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.CreateContainer(ctx, &req)
			},
			"UpdateContainer": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req UpdateContainerRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.UpdateContainer(ctx, &req)
			},
			"StopContainer": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req StopContainerRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.StopContainer(ctx, &req)
			},
			"StateChange": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req StateChangeEvent
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.StateChange(ctx, &req)
			},
			"AdjustPodSandboxNetwork": func(ctx context.Context, unmarshal func(interface{}) error) (interface{}, error) {
				var req AdjustPodSandboxNetworkRequest
				if err := unmarshal(&req); err != nil {
					return nil, err
				}
				return svc.AdjustPodSandboxNetwork(ctx, &req)
			},
		},
	})
}

type pluginClient struct {
	client *ttrpc.Client
}

func NewPluginClient(client *ttrpc.Client) PluginService {
	return &pluginClient{
		client: client,
	}
}

func (c *pluginClient) Configure(ctx context.Context, req *ConfigureRequest) (*ConfigureResponse, error) {
	var resp ConfigureResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "Configure", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) Synchronize(ctx context.Context, req *SynchronizeRequest) (*SynchronizeResponse, error) {
	var resp SynchronizeResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "Synchronize", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) Shutdown(ctx context.Context, req *Empty) (*Empty, error) {
	var resp Empty
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "Shutdown", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*CreateContainerResponse, error) {
	var resp CreateContainerResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "CreateContainer", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) UpdateContainer(ctx context.Context, req *UpdateContainerRequest) (*UpdateContainerResponse, error) {
	var resp UpdateContainerResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "UpdateContainer", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) StopContainer(ctx context.Context, req *StopContainerRequest) (*StopContainerResponse, error) {
	var resp StopContainerResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "StopContainer", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) StateChange(ctx context.Context, req *StateChangeEvent) (*Empty, error) {
	var resp Empty
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "StateChange", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *pluginClient) AdjustPodSandboxNetwork(ctx context.Context, req *AdjustPodSandboxNetworkRequest) (*AdjustPodSandboxNetworkResponse, error) {
	var resp AdjustPodSandboxNetworkResponse
	if err := c.client.Call(ctx, "nri.pkg.api.v1alpha1.Plugin", "AdjustPodSandboxNetwork", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
