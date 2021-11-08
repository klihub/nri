module github.com/containerd/nri

go 1.16

require (
	github.com/container-orchestrated-devices/container-device-interface v0.0.0-20220111162300-46367ec063fd
	// when updating containerd, adjust the replace rules accordingly
	github.com/containerd/containerd v1.5.5
	github.com/containerd/ttrpc v1.0.2
	github.com/containers/podman/v3 v3.2.0-rc1.0.20211005134800-8bcc086b1b9d
	github.com/gogo/protobuf v1.3.2
	github.com/opencontainers/runtime-spec v1.0.3-0.20210326190908-1c3f411f0417
	github.com/opencontainers/runtime-tools v0.9.0
	github.com/opencontainers/selinux v1.10.0
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.1
	golang.org/x/sys v0.0.0-20210906170528-6f6e22806c34
	k8s.io/cri-api v0.20.6
	sigs.k8s.io/yaml v1.3.0
)

replace (
	github.com/gogo/googleapis => github.com/gogo/googleapis v1.3.2
	github.com/golang/protobuf => github.com/golang/protobuf v1.3.5
	google.golang.org/genproto => google.golang.org/genproto v0.0.0-20200224152610-e50cd9704f63
	google.golang.org/grpc => google.golang.org/grpc v1.27.1
)
