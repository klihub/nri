module github.com/containerd/nri

go 1.14

require (
	// when updating containerd, adjust the replace rules accordingly
	github.com/containerd/containerd v1.5.0-beta.3
	github.com/containerd/ttrpc v1.0.2
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.4.3 // indirect
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.7.0 // indirect
	golang.org/x/sys v0.0.0-20210511113859-b0526f3d8744 // indirect
	k8s.io/cri-api v0.21.0
	sigs.k8s.io/yaml v1.2.0
)

replace (
	github.com/gogo/googleapis => github.com/gogo/googleapis v1.3.2
	github.com/golang/protobuf => github.com/golang/protobuf v1.3.5
	google.golang.org/genproto => google.golang.org/genproto v0.0.0-20200224152610-e50cd9704f63
	google.golang.org/grpc => google.golang.org/grpc v1.27.1
)
