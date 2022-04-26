## Background

This prototype aims to extend NRI beyond its current v1 capabilities. The
primary goal of the proposed extension is to allow NRI to act as a common
infrastructure for plugging in extensions to runtimes, and to allow these
extensions to implement, among other things, vendor- and domain-specific
custom logic for improved container configuration.


## Description

The prototype defines

  - a plugin API that plugins use to interact with NRI
  - a runtime API that runtimes use to interact with NRI
  - a stub that can be used as a library or template for writing plugins
  - sample plugins demonstrating some of NRI's extended capabilities

Using the plugin- and runtime-integration APIs allows one to hook plugins
into the lifecycle of containers managed by a runtime. These plugins can
then adjust a limited set of container parameters in connection with some
of the container's lifecycle state transitions.


### Revised Plugin API

The prototype changes both the lifecycle of plugins and the way plugins
are invoked and communicate with NRI. v1 used an OCI hook-like one-shot
invocation mechanism. There a separate instance of a plugin was spawned
for every event. This instance then used its standard input and output
to receive a request and provide a response, both as JSON data.

This extended version switches to daemon-like plugins. A single instance
of a plugin is now responsible for handling the full stream of events
intended for that plugin. A unix-domain socket is used as the transport
for communication. This both results in improved communication efficiency
with lower per-message overhead, and enables a more straightforward
implementation of stateful plugins, whenever this is necessary.

Instead of JSON requests and responses the extended version defines a
ttRPC-based protocol. This protocol provides the necessary services for
plugins to

  - register with NRI
  - optionally acquire plugin-specific configuration
  - subscribe for events of interest
  - update containers in response to events
  - update containers based on some event internal to the plugin

NRI allows one to tie the lifecycle of a plugin to that of the runtime
and NRI itself. Plugins placed in a well known NRI-specific location are
discovered and started during runtime startup, and stopped when the runtime
is shut down. These pre-launched plugins use a pre-connected socket for
communicating with NRI. NRI also allows plugins to establish connection
at a later phase with the intention to allow plugins to be managed as
Kubernetes deployment sets. Both pre-launched and dynamically connecting
plugins can be disabled in the NRI configuration.

#### Plugin Events

Plugins can subscribe to lifecycle events of pods and containers. The
available events are

  - pod creation
  - pod shutdown
  - pod removal
  - container creation
  - container startup
  - container update
  - container shutdown
  - container removal

Additionally there are a few related synthetic events available. These
are

  - post-create: container successfully created
  - post-start: container successfully started
  - post-update: container successfully updated

These are essentially notifications which allow a plugin to trigger any
related plugin-specific action or processing that falls outside the realm
or control of the runtime. Additionally, using these events a plugin can
introspect the final state of the associated container once all the active
plugins have processed the associated event.

Plugins can make changes to containers during a container's creation, update
and stop events. The extent of allowed changes depends on the event and the
state of the container. During container creation, the following parameters
can be adjusted for the container being created

  - labels, annotations
  - environment variables
  - set of OCI hooks
  - container mounts
  - devices available to the container
  - assigned resources (as defined by the OCI Spec)
  - container cgroup path

For other containers than the one being created and for any running container
during container update or shutdown event, the following parameters can be
updated:

  - assigned resources (as defined by the OCI Spec)

#### Plugin Invocation Order

During registration plugins assign themselves a plugin index within NRI.
This index determines the priority between plugins and is used to establish
a global ordering for plugin invocation. For each event all plugins which
subscribed for that event are invoked in increasing order of priority.

For every event which allows plugins to alter containers NRI verifies that
no accidental conflicting changes have been made by two or more plugins. If
such a conflict is detected the associated container state transition is
aborted with an error. There are occasions where plugins do need to modify
data added or altered by other plugins. The NRI protocol provides a simple
mechanism for plugins to indicate that they are consciously altering data
already touched by other plugins. Such changes are then allowed by NRI.

#### Initial Plugin State Synchronization

The NRI plugin API defines a synthetic event which is used synchronize the
state of a plugin with that of the runtime. This event is delivered as the
first one once a container is fully registered and configured in NRI. In
response to this event the plugin can update containers in order to bring
the overall running state to one which is considered valid by the plugin.

#### Plugin Stub

The plugin stub is a library which takes care of many of the low-level
details of implementing plugins, including connection establishment,
plugin registration and configuration, event subscription, etc. All of the
sample plugins are implemented using the stub so any of these can serve as
a tutorial on how to use the stub for writing plugins.


### Sample Plugins

The current prototype comes with the following sample plugins:

  - an OCI hook injector
  - a device injector for testing
  - a plugin event logger

The hook injector plugin uses podman's Hook Manager to perform OCI hook
injection to containers. While such support is already natively present
in CRI-O, it is missing from containerd. Loading this plugin to containerd
should enable CRI-O compatible hook injection in containerd as well.

The device injector plugin can inject devices into containers based on
pod- or container-level annotations.

The logger plugin is a plugins that simply logs all the events that
it receives without (usually) altering any container. It can be used
to inspect a processing chain of several plugins by injecting multiple
logger instances to various phases (priorities) in the chain of plugins.


## Building and Testing

If you are interested in trying out extended NRI plugins in practice,
you can do so by following the instruction in this chapter. These assume
that you have a Kubernetes cluster set up with CRI-O as the container
runtimes.

The necessary steps to get everything up an running are:

  - clone and build NRI repo, build sample plugins
  - clone and build CRI-O repo with NRI integration
  - replace stock CRI-O with built one
  - start plugins
  - create test/demo workloads

### Cloning NRI Repository

You can clone the NRI repository with the following command:

```
    git clone -b pr/proto/draft https://github.com/klihub/nri
```

Before to compile any plugins, you need to have these installed on your
system

  - recent golang toolchain (>= 1.16 is recommended)
  - GNU Make
  - protoc compiler
  - protoc development packages

### Building NRI Sample Plugins

You can generate the plugin API and build the plugins using the following
commands:

```
    cd nri
    make install-ttrpc-plugin
    make install-proto-dependencies
    make all
```

This should generate the following binaries in the `bin` directory:

  - device-injector
  - hook-injector
  - logger

### Building CRI-O With NRI Support

You can clone and build CRI-O patched to support NRI using these
commands:

```
    git clone -b pr/proto/nri https://github.com/klihub/cri-o
    cd cri-o
    make
```

Prior to building, make sure that all the necessary dependencies for
building CRI-O are installed. Once it builds successfully, you should be
left with an NRI-enabled crio version in the directory `bin`. This version
accepts a newly added `--enable-nri` command line option which can be used
to start CRI-O with NRI enabled.

Alternatively you can generate a configuration file that enables NRI and
replace you current CRI-O configuration with that. You can use the following
commands to do that.

```
    mkdir /etc/nri
    touch /etc/nri/nri.conf
    CRIOCONF=/etc/crio/crio.conf
    ./bin/crio config > $CRIOCONF.orig
    ./bin/crio --config $CRIOCONF.orig --enable-nri config > $CRIOCONF
```

### Configuring NRI

For this demo setup and our tests, we want to start NRI plugins by hand.
The plugins will need to connect dynamically to NRI as they start so we'll
explicitly enable dynamic connections in the configuration. You can do this
with the following commands:

```
    echo "disablePluginConnections: false" > /etc/nri/nri.conf
```

### Running Tests

The first test demonstrates CDI device injection by annotations.

Here is our sample PodSpec for our demo workloads. These annotate containers
for device injection.

```
$ cat device-demo.yaml
    apiVersion: v1
    kind: Pod
    metadata:
      name: test0
      labels:
        app: test
      annotations:
        devices.nri.io/container.test0c0: |
          - path: "/dev/zero-dev"
            type: "c"
            major: 1
            minor: 5
        devices.nri.io/container.test0c1: |
          - path: "/dev/null-dev"
            type: "c"
            major: 1
            minor: 3
        devices.nri.io/container.test0c2: |
          - path: "/dev/null-dev"
            type: "c"
            major: 1
            minor: 3
          - path: "/dev/zero-dev"
            type: "c"
            major: 1
            minor: 5
    spec:
      containers:
      - name: test0c0
        image: busybox
        imagePullPolicy: IfNotPresent
        command:
          - sh
          - -c
          - echo test0c0 $(sleep inf)
        resources:
          requests:
            cpu: 500m
            memory: '100M'
          limits:
            cpu: 500m
            memory: '100M'
      - name: test0c1
        image: busybox
        imagePullPolicy: IfNotPresent
        command:
          - sh
          - -c
          - echo test0c1 $(sleep inf)
        resources:
          requests:
            cpu: 500m
            memory: '100M'
          limits:
            cpu: 500m
            memory: '100M'
      - name: test0c2
        image: busybox
        imagePullPolicy: IfNotPresent
        command:
          - sh
          - -c
          - echo test0c2 $(sleep inf)
        resources:
          requests:
            cpu: 500m
            memory: '100M'
          limits:
            cpu: 500m
            memory: '100M'
      terminationGracePeriodSeconds: 1
```

For running the tests you'll need to do the following. You can do these using the
commands below.

  - start CRI-O with NRI enabled
  - start each plugin hooking it to event processing in the correct order
  - create the workload pods
  - inspect the resulting containers

Replace cri-o with the NRI-enabled one:

```
    systemctl stop crio
    cd cri-o; ./bin/crio --enable-nri
```

Start device injector plugin (run this in a separate shell):

```
    cd nri
    ./v2alpha1/bin/device-injector -idx 10
```

Start logger to inspect device injection (run this in a separate shell):

```
    cd nri
    ./v2alpha1/bin/logger -idx 20
```

Create test workloads:

```
    cd nri
    kubectl apply -f ./v2alpha1/examples/pod-specs/device-demo.yaml
```

What should happen here is the following:

  1. device injector plugin injects the annotated devices
  2. logger logs the result of this
  3. the container(s) get created with the devices injected

Inspect the results. You should see something similar to the output below.

```
#
# You can inspect the results using crictl and container IDs with something
# like this:
#   for ctr in $(crictl -r /var/run/crio/crio.sock ps | grep test0c. | \
#                    tr -s '\t ' ' ' | cut -d ' ' -f1); do \
#       echo "***** container $ctr *****"; \
#       crictl -r /var/run/crio/crio.sock exec $ctr /bin/sh -c 'ls -ls /dev'; \
#   done
#
# Or then using kubectl...
#
# kubectl exec test0 -c test0c0 -- /bin/sh -c 'ls -ls /dev'
total 0
     0 lrwxrwxrwx    1 root     root            11 Nov  8 12:48 core -> /proc/kcore
     0 lrwxrwxrwx    1 root     root            13 Nov  8 12:48 fd -> /proc/self/fd
     0 crw-rw-rw-    1 root     root        1,   7 Nov  8 12:48 full
     0 drwxrwxrwt    2 root     root            40 Nov  8 12:48 mqueue
     0 crw-rw-rw-    1 root     root        1,   3 Nov  8 12:48 null
     0 crw-rw-rw-    1 root     root        1,   3 Nov  8 12:48 null-dev
     0 lrwxrwxrwx    1 root     root             8 Nov  8 12:48 ptmx -> pts/ptmx
     0 drwxr-xr-x    2 root     root             0 Nov  8 12:48 pts
     0 crw-rw-rw-    1 root     root        1,   8 Nov  8 12:48 random
     0 drwxrwxrwt    2 root     root            40 Nov  8 12:48 shm
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stderr -> /proc/self/fd/2
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stdin -> /proc/self/fd/0
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stdout -> /proc/self/fd/1
     0 -rw-rw-rw-    1 root     root             0 Nov  8 12:48 termination-log
     0 crw-rw-rw-    1 root     root        5,   0 Nov  8 12:48 tty
     0 crw-rw-rw-    1 root     root        1,   9 Nov  8 12:48 urandom
     0 crw-rw-rw-    1 root     root        1,   5 Nov  8 12:48 zero

# kubectl exec test0 -c test0c1 -- /bin/sh -c 'ls -ls /dev'
total 0
     0 lrwxrwxrwx    1 root     root            11 Nov  8 12:48 core -> /proc/kcore
     0 lrwxrwxrwx    1 root     root            13 Nov  8 12:48 fd -> /proc/self/fd
     0 crw-rw-rw-    1 root     root        1,   7 Nov  8 12:48 full
     0 drwxrwxrwt    2 root     root            40 Nov  8 12:48 mqueue
     0 crw-rw-rw-    1 root     root        1,   3 Nov  8 12:48 null
     0 lrwxrwxrwx    1 root     root             8 Nov  8 12:48 ptmx -> pts/ptmx
     0 drwxr-xr-x    2 root     root             0 Nov  8 12:48 pts
     0 crw-rw-rw-    1 root     root        1,   8 Nov  8 12:48 random
     0 drwxrwxrwt    2 root     root            40 Nov  8 12:48 shm
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stderr -> /proc/self/fd/2
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stdin -> /proc/self/fd/0
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stdout -> /proc/self/fd/1
     0 -rw-rw-rw-    1 root     root             0 Nov  8 12:48 termination-log
     0 crw-rw-rw-    1 root     root        5,   0 Nov  8 12:48 tty
     0 crw-rw-rw-    1 root     root        1,   9 Nov  8 12:48 urandom
     0 crw-rw-rw-    1 root     root        1,   5 Nov  8 12:48 zero
     0 crw-rw-rw-    1 root     root        1,   5 Nov  8 12:48 zero-dev

# kubectl exec test0 -c test0c2 -- /bin/sh -c 'ls -ls /dev'
total 0
     0 lrwxrwxrwx    1 root     root            11 Nov  8 12:48 core -> /proc/kcore
     0 lrwxrwxrwx    1 root     root            13 Nov  8 12:48 fd -> /proc/self/fd
     0 crw-rw-rw-    1 root     root        1,   7 Nov  8 12:48 full
     0 drwxrwxrwt    2 root     root            40 Nov  8 12:48 mqueue
     0 crw-rw-rw-    1 root     root        1,   3 Nov  8 12:48 null
     0 crw-rw-rw-    1 root     root        1,   3 Nov  8 12:48 null-dev
     0 lrwxrwxrwx    1 root     root             8 Nov  8 12:48 ptmx -> pts/ptmx
     0 drwxr-xr-x    2 root     root             0 Nov  8 12:48 pts
     0 crw-rw-rw-    1 root     root        1,   8 Nov  8 12:48 random
     0 drwxrwxrwt    2 root     root            40 Nov  8 12:48 shm
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stderr -> /proc/self/fd/2
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stdin -> /proc/self/fd/0
     0 lrwxrwxrwx    1 root     root            15 Nov  8 12:48 stdout -> /proc/self/fd/1
     0 -rw-rw-rw-    1 root     root             0 Nov  8 12:48 termination-log
     0 crw-rw-rw-    1 root     root        5,   0 Nov  8 12:48 tty
     0 crw-rw-rw-    1 root     root        1,   9 Nov  8 12:48 urandom
     0 crw-rw-rw-    1 root     root        1,   5 Nov  8 12:48 zero
     0 crw-rw-rw-    1 root     root        1,   5 Nov  8 12:48 zero-dev
```

### Running the Tests Using Pre-connected Plugins

Instead of running the plugins manually, you can also set them up as pre-
connected NRI plugins which are then started automatically whenever the
runtime is started up with NRI enabled. To do this, you need to place
symbolic links to the plugins you want to enable in the default NRI plugin
directory `/opt/nri/plugins` and restart `cri-o`. The names chosen for the
symbolic links should follow the pattern `<idx>-<pluginname>` to let NRI
determine the proper ordering for plugin invocation. For our demo this can
be accomplished with the following commands:

```
    cd nri
    mkdir -p /usr/local/bin
    cp bin/* /usr/local/bin
    mkdir -p /opt/nri/plugins
    ln -s /usr/local/bin/device-injector /opt/nri/plugins/10-device-injector
    cd ../cri-o
    ./bin/crio --enable-nri
```

When examining the resulting containers with the same `kubectl exec` commands
as before, you should see identical results.

If for testing purposes you also would like to create pre-connected logger
instances, you can create the necessary symbolic links to them the same way
as for the device injector and cdi resolver plugins. Additionally, you can
provide instance-specific configuration files for them to redirect their
output to instance-specific log files. You can do this using the following
commands:

```
    mkdir -p /etc/nri/conf.d
    ln -s /usr/local/bin/logger /opt/nri/plugins/20-logger
    cat > /etc/nri/conf.d/20-logger.conf <<EOF
    logFile: /tmp/nri-post-device-injection.log
    events:
      - CreateContainer
      - PostStartContainer
    EOF
    # stop crio, then
    ./bin/crio --enable-nri
    # Delete and restart the demo workload
    kubectl delete pod test0
    kubectl apply -f ../nri/v2alpha1/examples/pod-specs/cdi-demo.yaml
```

You should now see the corresponding event logs in the configured
`/tmp/nri-post-device-injection.log` file.

### Building Containerd With NRI Support

You can clone and build containerd patched to support NRI using these
commands:

```
    git clone -b pr/proto/nri https://github.com/klihub/containerd
    cd containerd
    make
```

Prior to building, make sure that all the necessary dependencies for
building containerd are installed. Once it builds successfully, you should
be left with an NRI-enabled containerd version in the directory `bin`. This
version accepts a few newly added configuration options for controlling NRI.

You need to generate a containerd configuration file (if you don't have one
yet) and enable support for NRI, which is otherwise disabled by default.
You can do this by first dumping your current configuration with the following
commands:

```
    mkdir /etc/nri
    touch /etc/nri/nri.conf
    CONTAINERDCONF=/etc/containerd/config.toml
    ./bin/containerd config dump > $CONTAINERDCONF.orig
    ./bin/containerd config dump > $CONTAINERDCONF
```

Then add the following CRI plugin NRI configuration fragment to enable NRI
support:

```
[plugins."io.containerd.grpc.v1.cri".nri]
    enable = true
```

Now when you start up containerd you should see log messages indicating the
NRI has been enabled and activated.

### Running Tests

Our test for containerd demonstrates crio-compatible OCI hook injection
using and NRI plugin. This NRI plugin uses podman's Hook Manager to do
its deed.

First let's check that our OCI demo hook and its configuration are in
place.

```
    # from ../nri/v2alpha1/examples/etc/containers/oci/hooks.d/demo-hook.json
    cat /etc/containers/oci/hooks.d/demo-hook.json
    {
        "version": "1.0.0",
        "hook": {
            "path": "/usr/local/sbin/demo-hook.sh",
            "args": ["this", "is", "a", "demo-hook", "invocation"],
            "env": [
                "DEMO_HOOK_VAR1=value1",
                "DEMO_HOOK_VAR2=value2"
            ]
        },
        "when": {
            "annotations": {
                "hookdemo": "true"
            }
        },
        "stages": ["prestart", "poststop"]
    }

    # from ../nri/v2alpha1/examples/usr/local/sbin/demo-hook.sh
    cat /usr/local/sbin/demo-hook.sh
    #!/bin/sh

    LOG=/tmp/demo-hook.log

    touch $LOG
    echo "========== [pid $$] $(date) ==========" >> $LOG
    echo "command: $0 $@" >> $LOG
    echo "environment:" >> $LOG
    env | sed 's/^/    /g' >> $LOG
```

Here is how our demo PodSpec looks like:

```
    # from ../nri/v2alpha1/examples/pod-specs/hook-demo.yaml
    cat ../nri/v2alpha1/examples/pod-specs/hook-demo.yaml
    apiVersion: v1
    kind: Pod
    metadata:
      name: hookdemo
      annotations:
        hookdemo: "true"
    spec:
      containers:
      - name: c0
        image: busybox
        imagePullPolicy: IfNotPresent
        command:
          - sh
          - -c
          - echo c0 $(sleep inf)
        resources:
          requests:
            cpu: 500m
            memory: '100M'
          limits:
            cpu: 500m
            memory: '100M'
      - name: c1
        image: busybox
        imagePullPolicy: IfNotPresent
        command:
          - sh
          - -c
          - echo c1 $(sleep inf)
        resources:
          requests:
            cpu: 500m
            memory: '100M'
          limits:
            cpu: 500m
            memory: '100M'
      terminationGracePeriodSeconds: 1
```

For the test, we need to run our hook injector plugin. To see how the NRI
request processing results look like we can also plug in a logger there,
after the injector:

```
    # Start the hook injector in one shell with this command...
    ../nri/bin/hook-injector -idx 10 -verbose
    # And the logger in another shell with this one...
    ../nri/bin/logger -idx 99
```

Now let's create the pod, wait for it to get up and running, then stop it.
This should have trigger the demo hook twice: first as a prestart hook and
a second time as a poststop hook.

```
   kubectl apply -f hook-demo.yaml
   sleep 5
   kubectl delete -f hook-demo.yaml
```

If everything was successful, we should see something like this logged by
the hook injector:

```
    ...
    INFO   [0185] hookdemo/c0: ContainerAdjustment:
    INFO   [0185] hookdemo/c0:    hooks:
    INFO   [0185] hookdemo/c0:      poststop:
    INFO   [0185] hookdemo/c0:      - args:
    INFO   [0185] hookdemo/c0:        - this
    INFO   [0185] hookdemo/c0:        - is
    INFO   [0185] hookdemo/c0:        - a
    INFO   [0185] hookdemo/c0:        - demo-hook
    INFO   [0185] hookdemo/c0:        - invocation
    INFO   [0185] hookdemo/c0:        env:
    INFO   [0185] hookdemo/c0:        - DEMO_HOOK_VAR1=value1
    INFO   [0185] hookdemo/c0:        - DEMO_HOOK_VAR2=value2
    INFO   [0185] hookdemo/c0:        path: /usr/local/sbin/demo-hook.sh
    INFO   [0185] hookdemo/c0:      prestart:
    INFO   [0185] hookdemo/c0:      - args:
    INFO   [0185] hookdemo/c0:        - this
    INFO   [0185] hookdemo/c0:        - is
    INFO   [0185] hookdemo/c0:        - a
    INFO   [0185] hookdemo/c0:        - demo-hook
    INFO   [0185] hookdemo/c0:        - invocation
    INFO   [0185] hookdemo/c0:        env:
    INFO   [0185] hookdemo/c0:        - DEMO_HOOK_VAR1=value1
    INFO   [0185] hookdemo/c0:        - DEMO_HOOK_VAR2=value2
    INFO   [0185] hookdemo/c0:        path: /usr/local/sbin/demo-hook.sh
    ...
    INFO   [0185] hookdemo/c1: ContainerAdjustment:
    INFO   [0185] hookdemo/c1:    hooks:
    INFO   [0185] hookdemo/c1:      poststop:
    INFO   [0185] hookdemo/c1:      - args:
    INFO   [0185] hookdemo/c1:        - this
    INFO   [0185] hookdemo/c1:        - is
    INFO   [0185] hookdemo/c1:        - a
    INFO   [0185] hookdemo/c1:        - demo-hook
    INFO   [0185] hookdemo/c1:        - invocation
    INFO   [0185] hookdemo/c1:        env:
    INFO   [0185] hookdemo/c1:        - DEMO_HOOK_VAR1=value1
    INFO   [0185] hookdemo/c1:        - DEMO_HOOK_VAR2=value2
    INFO   [0185] hookdemo/c1:        path: /usr/local/sbin/demo-hook.sh
    INFO   [0185] hookdemo/c1:      prestart:
    INFO   [0185] hookdemo/c1:      - args:
    INFO   [0185] hookdemo/c1:        - this
    INFO   [0185] hookdemo/c1:        - is
    INFO   [0185] hookdemo/c1:        - a
    INFO   [0185] hookdemo/c1:        - demo-hook
    INFO   [0185] hookdemo/c1:        - invocation
    INFO   [0185] hookdemo/c1:        env:
    INFO   [0185] hookdemo/c1:        - DEMO_HOOK_VAR1=value1
    INFO   [0185] hookdemo/c1:        - DEMO_HOOK_VAR2=value2
    INFO   [0185] hookdemo/c1:        path: /usr/local/sbin/demo-hook.sh
```

You should also see these injected in the containers as logger by the logger:

```
    ...
    INFO   [0183] StartContainer:    - KUBERNETES_SERVICE_PORT=443
    INFO   [0183] StartContainer:    - KUBERNETES_SERVICE_PORT_HTTPS=443
    INFO   [0183] StartContainer:    - KUBERNETES_PORT=tcp://10.96.0.1:443
    INFO   [0183] StartContainer:    - KUBERNETES_PORT_443_TCP=tcp://10.96.0.1:443
    INFO   [0183] StartContainer:    - KUBERNETES_PORT_443_TCP_PROTO=tcp
    INFO   [0183] StartContainer:    - KUBERNETES_PORT_443_TCP_PORT=443
    INFO   [0183] StartContainer:    hooks:
    INFO   [0183] StartContainer:      poststop:
    INFO   [0183] StartContainer:      - args:
    INFO   [0183] StartContainer:        - this
    INFO   [0183] StartContainer:        - is
    INFO   [0183] StartContainer:        - a
    INFO   [0183] StartContainer:        - demo-hook
    INFO   [0183] StartContainer:        - invocation
    INFO   [0183] StartContainer:        env:
    INFO   [0183] StartContainer:        - DEMO_HOOK_VAR1=value1
    INFO   [0183] StartContainer:        - DEMO_HOOK_VAR2=value2
    INFO   [0183] StartContainer:        path: /usr/local/sbin/demo-hook.sh
    INFO   [0183] StartContainer:      prestart:
    INFO   [0183] StartContainer:      - args:
    INFO   [0183] StartContainer:        - this
    INFO   [0183] StartContainer:        - is
    INFO   [0183] StartContainer:        - a
    INFO   [0183] StartContainer:        - demo-hook
    INFO   [0183] StartContainer:        - invocation
    INFO   [0183] StartContainer:        env:
    INFO   [0183] StartContainer:        - DEMO_HOOK_VAR1=value1
    INFO   [0183] StartContainer:        - DEMO_HOOK_VAR2=value2
    INFO   [0183] StartContainer:        path: /usr/local/sbin/demo-hook.sh
    INFO   [0183] StartContainer:    id: d7212a738ca3a98881ba4ec00e17b692aae295f57127955feff923967e180d6
    INFO   [0183] StartContainer:    labels:
    INFO   [0183] StartContainer:      io.cri-containerd.kind: container
    ...
```

You should also see that the demo hook has logged both its prestart and
poststop invocations in its log file under /tmp/demo-hook.log, once for
c0 and once for c1:

```
    cat /tmp/demo-hook.log
    ========== [pid 241998] Tue Nov 30 13:48:39 UTC 2021 ==========
    command: /usr/local/sbin/demo-hook.sh is a demo-hook invocation
    environment:
        PWD=/run/containerd/io.containerd.runtime-shim.v2.shim/k8s.io/11ca9e74a34fa7313d340aab6ff48ff4b50822ecc62656abcee27f739ba60359
        SHLVL=0
        DEMO_HOOK_VAR1=value1
        DEMO_HOOK_VAR2=value2
        _=/usr/bin/env
    ========== [pid 242035] Tue Nov 30 13:48:39 UTC 2021 ==========
    command: /usr/local/sbin/demo-hook.sh is a demo-hook invocation
    environment:
        PWD=/run/containerd/io.containerd.runtime-shim.v2.shim/k8s.io/d7212a738ca3a98881ba4ec00e17b692aae295f57127955feff923967e180d66
        SHLVL=0
        DEMO_HOOK_VAR1=value1
        DEMO_HOOK_VAR2=value2
        _=/usr/bin/env
    ========== [pid 242375] Tue Nov 30 14:03:54 UTC 2021 ==========
    command: /usr/local/sbin/demo-hook.sh is a demo-hook invocation
    environment:
        PWD=/run/containerd/io.containerd.runtime-shim.v2.shim/k8s.io/a7198db29a7af7edc5b0b09adfffbce76f02c04528744f947ef0dcd1c42e10a5
        SHLVL=0
        DEMO_HOOK_VAR1=value1
        DEMO_HOOK_VAR2=value2
        _=/usr/bin/env
    ========== [pid 242388] Tue Nov 30 14:03:54 UTC 2021 ==========
    command: /usr/local/sbin/demo-hook.sh is a demo-hook invocation
    environment:
        PWD=/run/containerd/io.containerd.runtime-shim.v2.shim/k8s.io/a7198db29a7af7edc5b0b09adfffbce76f02c04528744f947ef0dcd1c42e10a5
        SHLVL=0
        DEMO_HOOK_VAR1=value1
        DEMO_HOOK_VAR2=value2
        _=/usr/bin/env
```
