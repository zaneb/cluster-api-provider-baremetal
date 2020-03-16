# Cluster API Provider for Managed Bare Metal Hardware

This repository contains a Machine actuator implementation for the
Kubernetes [Cluster API](https://github.com/kubernetes-sigs/cluster-api/).

For more information about this actuator and related repositories, see
[metal3.io](http://metal3.io/).

## Development Environment

* See [metal3-dev-env](https://github.com/metal3-io/metal3-dev-env) for an
  end-to-end development and test environment for
  `cluster-api-provider-baremetal` and
  [baremetal-operator](https://github.com/metal3-io/baremetal-operator).
* [Setting up for tests](docs/dev/setup.md)
* Using [Minikube](docs/dev/minikube.md)
* Using [OpenShift 4](docs/dev/openshift.md)

## API

See the [API Documentation](docs/api.md) for details about the `providerSpec`
API used with this `cluster-api` provider.

## MachineSet Scaling

If you would like a MachineSet to be automatically scaled to the number of
matching BareMetalHosts, annotate that MachineSet with key
`metal3.io/autoscale-to-hosts` and any value.

When reconciling a MachineSet, the controller will count all of the
BareMetalHosts that either:
* match the MachineSet's `Spec.Template.Spec.ProviderSpec.HostSelector` and
  have a ConsumerRef that is `nil`
* has a ConsumerRef that references a Machine that is part of the MachineSet

This ensures that in case a BareMetalHost has previously been consumed by a
Machine, but either labels or selectors have since been changed, it will
continue to get counted with the MachineSet that its Machine belongs to.
