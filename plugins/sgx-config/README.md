## SGX Resource Configurator Plugin

This sample plugin can configure SGX resources using annotations.

### SGX Annotations

You can annotate SGX EPC configuration for every container in the pod,
or just a specified container using the following annotation notations:
```
...
metadata:
  annotations:
    # for all containers in the pod
    sgx-epc.nri.io/pod: "32768"
    # alternative notation for all containers in the pod
    sgx-epc.nri.io: "8192"
    # for container c0 in the pod
    sgx-epc.nri.io/container.c0: "16384"
...
```

## Testing

You can test this plugin using a kubernetes cluster/node with a container
runtime that has NRI support enabled. Start the plugin on the target node
(`sgx-config -idx 10`), create a pod with some SGX annotation, then verify
that the annotations SGC resource configuration gets properly set.
