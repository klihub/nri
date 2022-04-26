module github.com/containerd/nri

go 1.16

require (
	// when updating containerd, adjust the replace rules accordingly
	github.com/containerd/containerd v1.6.0
	github.com/containerd/ttrpc v1.1.0
	github.com/opencontainers/runtime-spec v1.0.3-0.20210326190908-1c3f411f0417
	github.com/pkg/errors v0.9.1
	google.golang.org/protobuf v1.27.1
	k8s.io/cri-api v0.23.1
)

replace github.com/containerd/ttrpc v1.1.0 => github.com/containerd/ttrpc v0.0.0-20220421210857-74421d10189e
