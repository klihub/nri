module github.com/containerd/nri

go 1.16

require (
	// when updating containerd, adjust the replace rules accordingly
	github.com/containerd/containerd v1.6.0
	github.com/containerd/ttrpc v1.1.0
	github.com/containers/podman/v3 v3.2.0-rc1.0.20211005134800-8bcc086b1b9d
	github.com/moby/sys/mountinfo v0.5.0
	github.com/onsi/ginkgo/v2 v2.1.4
	github.com/onsi/gomega v1.19.0
	github.com/opencontainers/runtime-spec v1.0.3-0.20210326190908-1c3f411f0417
	github.com/opencontainers/runtime-tools v0.9.0
	github.com/pkg/errors v0.9.1
	github.com/r3labs/diff/v3 v3.0.0
	github.com/sirupsen/logrus v1.8.1
	github.com/sters/yaml-diff v0.4.0
	github.com/stretchr/testify v1.7.0
	golang.org/x/sys v0.0.0-20220319134239-a9b59b0215f8
	google.golang.org/protobuf v1.27.1
	k8s.io/cri-api v0.23.1
	sigs.k8s.io/yaml v1.2.0
)

replace (
	github.com/containerd/ttrpc v1.1.0 => github.com/containerd/ttrpc v0.0.0-20220421210857-74421d10189e
	github.com/opencontainers/runtime-tools v0.9.0 => github.com/opencontainers/runtime-tools v0.0.0-20220125021840-0105384f68e1
)
