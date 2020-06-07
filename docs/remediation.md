# Baremetal Machine Remediation

## Summary

It is not always practical to require admin intervention once a node has been
identified as having reached a bad or unknown state.

In order to automate the recovery of exclusive workloads (eg. RWO volumes and
StatefulSets), we need a way to put failed nodes into a safe state, indicate to
the scheduler that affected workloads can be started elsewhere, and then
attempt to recover capacity.

## Motivation

Hardware is imperfect, and software contains bugs. When node level failures
such as kernel hangs or dead NICs occur, the work required from the cluster
does not decrease - workloads from affected nodes need to be restarted
somewhere. 

However some workloads may require at-most-one semantics. Failures affecting
these kind of workloads risk data loss and/or corruption if "lost" nodes are
assumed to be dead when they are still running.  For this reason it is
important to know that the node has reached a safe state before initiating
recovery of the workload.

Powering off the affected node via IPMI or related technologies achieves this,
but must be paired with deletion of the Node object to signal to the scheduler
that no Pods or PersistentVolumes are present there.

Ideally customers would over-provision the cluster so that a node failure (or
several) does not put additional stress on surviving peers, however budget
constraints mean that this is often not the case, particularly in Edge
deployments which may consist of as few as three nodes of commodity hardware. 
Even when deployments start off over-provisioned, there is a tendency for the
extra capacity to become permanently utilised.  It is therefore usually
important to recover the lost capacity quickly.

For this reason it is desirable to power the machine back on again, in the hope
that the problem was transient and the node can return to a healthy state.
Upon restarting, kubelet automatically contacts the masters to re-register
itself, allowing it to host workloads.

## Implementation Details

The remediation logic is implemented in the actuator, which watches for the
presence of a `host.metal3.io/external-remediation` Machine
annotation. If present, the actuator locates the Machine
and BareMetalHost host objects via their annotations, and annotates the BareMetalHost
CR with a `reboot.metal3.io/capbm-requested-power-off` to power cycle the host by
utilizing the [Baremetal-Operator reboot API](https://github.com/metal3-io/metal3-docs/blob/master/design/reboot-interface.md).
Upon host power off, the controller deletes the node and then removes the annotation,
allowing it to be powered back on.

## Remediation Flow

Remediation steps (triggered by `host.metal3.io/external-remediation` annotation on the Machine):
1) Power off the host associated with unhealthy Machine (by adding
`reboot.metal3.io/capbm-requested-power-off` annotation)
2) Add `remediation.metal3.io/powered-off-for-remediation` annotation to the unhealthy Machine
3) Delete the node
4) Power on the host
5) Wait for the node the come up (by waiting for the node to be registered in the cluster)
6) Remove `remediation.metal3.io/powered-off-for-remediation` annotation and the MAO's unhealthy
machine annotation

## Assumptions

MHC will delegate all external remediation responsibility, without any constraints
or limitations. This will allow CAPBM to decide which machines to remediate and how.
This include masters nodes.

## Dependencies

This design is intended to integrate with OpenShift’s [Machine Healthcheck
implementation](https://github.com/openshift/machine-api-operator/blob/master/pkg/controller/machinehealthcheck/machinehealthcheck_controller.go#L407)
 and [Baremetal Operator Reboot API](https://github.com/metal3-io/baremetal-operator/pull/424)