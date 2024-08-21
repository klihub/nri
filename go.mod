module github.com/containerd/nri

go 1.20

require (
	github.com/containerd/ttrpc v1.2.3
	github.com/moby/sys/mountinfo v0.6.2
	github.com/onsi/ginkgo/v2 v2.5.0
	github.com/onsi/gomega v1.24.0
	github.com/opencontainers/runtime-spec v1.0.3-0.20220825212826-86290f6a00fb
	github.com/opencontainers/runtime-tools v0.9.0
	github.com/sirupsen/logrus v1.9.3
	github.com/stretchr/testify v1.8.2
	golang.org/x/sys v0.18.0
	google.golang.org/grpc v1.57.1
	google.golang.org/protobuf v1.33.0
	k8s.io/cri-api v0.25.3
	sigs.k8s.io/yaml v1.3.0
)

require (
	github.com/containerd/log v0.1.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/syndtr/gocapability v0.0.0-20200815063812-42c35b437635 // indirect
	golang.org/x/net v0.23.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230731190214-cbb8c96f2d6d // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/opencontainers/runtime-tools v0.9.0 => github.com/opencontainers/runtime-tools v0.0.0-20221026201742-946c877fa809

replace github.com/containerd/ttrpc => github.com/klihub/ttrpc v0.0.0-20240823054037-22688637a972
