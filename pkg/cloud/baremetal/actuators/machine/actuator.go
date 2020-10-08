/*
Copyright 2019 The Kubernetes authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	bmh "github.com/metal3-io/baremetal-operator/pkg/apis/metal3/v1alpha1"
	"github.com/metal3-io/baremetal-operator/pkg/utils"
	bmv1alpha1 "github.com/openshift/cluster-api-provider-baremetal/pkg/apis/baremetal/v1alpha1"
	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	machineapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	gherrors "github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	ProviderName = "baremetal"
	// HostAnnotation is the key for an annotation that should go on a Machine to
	// reference what BareMetalHost it corresponds to.
	HostAnnotation                  = "metal3.io/BareMetalHost"
	requeueAfter                    = time.Second * 30
	externalRemediationAnnotation   = "host.metal3.io/external-remediation"
	poweredOffForRemediation        = "remediation.metal3.io/powered-off-for-remediation"
	requestPowerOffAnnotation       = "reboot.metal3.io/capbm-requested-power-off"
	nodeLabelsBackupAnnotation      = "remediation.metal3.io/node-labels-backup"
	nodeAnnotationsBackupAnnotation = "remediation.metal3.io/node-annotations-backup"
	nodeFinalizer                   = "metal3.io/capbm"
)

// Add RBAC rules to access cluster-api resources
//+kubebuilder:rbac:groups=cluster.k8s.io,resources=machines;machines/status,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cluster.k8s.io,resources=machineClasses,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=nodes;events,verbs=get;list;watch;create;update;patch;delete

// RBAC to access BareMetalHost resources from metal3.io
//+kubebuilder:rbac:groups=metal3.io,resources=baremetalhosts,verbs=get;list;watch;update;patch

// Actuator is responsible for performing machine reconciliation
type Actuator struct {
	client client.Client
}

// ActuatorParams holds parameter information for Actuator
type ActuatorParams struct {
	Client client.Client
}

// NewActuator creates a new Actuator
func NewActuator(params ActuatorParams) (*Actuator, error) {
	return &Actuator{
		client: params.Client,
	}, nil
}

// Create creates a machine and is invoked by the Machine Controller
func (a *Actuator) Create(ctx context.Context, machine *machinev1beta1.Machine) error {
	log.Printf("Creating machine %v .", machine.Name)

	// load and validate the config
	if machine.Spec.ProviderSpec.Value == nil {
		return a.setError(ctx, machine, "ProviderSpec is missing")
	}
	config, err := configFromProviderSpec(machine.Spec.ProviderSpec)
	if err != nil {
		log.Printf("Error reading ProviderSpec for machine %s: %s", machine.Name, err.Error())
		return err
	}
	err = config.IsValid()
	if err != nil {
		return a.setError(ctx, machine, err.Error())
	}

	// clear an error if one was previously set
	err = a.clearError(ctx, machine)
	if err != nil {
		return err
	}

	// look for associated BMH
	host, err := a.getHost(ctx, machine)
	if err != nil {
		return err
	}

	// none found, so try to choose one
	if host == nil {
		host, err = a.chooseHost(ctx, machine)
		if err != nil {
			return err
		}
		if host == nil {
			log.Printf("No available host found. Requeuing.")
			return &machineapierrors.RequeueAfterError{RequeueAfter: requeueAfter}
		}
		log.Printf("Associating machine %s with host %s", machine.Name, host.Name)
	} else {
		log.Printf("Machine %s already associated with host %s", machine.Name, host.Name)
	}

	// Add a finalizer for the BMH. This will allow us to delete
	// the Machine when the BMH is deleted.
	if !utils.StringInList(host.Finalizers, machinev1beta1.MachineFinalizer) {
		host.Finalizers = append(host.Finalizers, machinev1beta1.MachineFinalizer)
	}

	err = a.setHostSpec(ctx, host, machine, config)
	if err != nil {
		return err
	}

	_, err = a.ensureAnnotation(ctx, machine, host)
	if err != nil {
		return err
	}

	if err := a.handleNodeFinalizer(ctx, machine); err != nil {
		return err
	}

	if err := a.updateMachineStatus(ctx, machine, host); err != nil {
		return err
	}

	log.Printf("Finished creating machine %v .", machine.Name)
	return nil
}

// Delete deletes a machine and is invoked by the Machine Controller
func (a *Actuator) Delete(ctx context.Context, machine *machinev1beta1.Machine) error {
	log.Printf("Deleting machine %v .", machine.Name)

	if err := a.removeNodeFinalizer(ctx, machine); err != nil {
		return err
	}

	host, err := a.getHost(ctx, machine)
	if err != nil {
		return err
	}

	if host == nil {
		log.Printf("finished deleting machine %v.", machine.Name)
		return nil
	}

	log.Printf("deleting machine %v using host %v", machine.Name, host.Name)

	if host.Spec.ConsumerRef == nil {
		log.Printf("removing host annotation from machine %v", machine.Name)
		_, err := a.clearAnnotation(ctx, machine)
		if err != nil {
			return err
		}
		return nil // updating the machine will re-enqueue it without us asking
	}

	// don't remove the ConsumerRef if it references some other
	// machine, but remove the annotation linking this machine to the
	// host so the current machine can continue deleting
	if !consumerRefMatches(host.Spec.ConsumerRef, machine) {
		log.Printf("host associated with %v, not machine %v.",
			host.Spec.ConsumerRef.Name, machine.Name)
		_, err := a.clearAnnotation(ctx, machine)
		if err != nil {
			return err
		}
		return nil
	}

	if host.Spec.Image != nil || host.Spec.Online || host.Spec.UserData != nil {
		log.Printf("starting to deprovision host %v", host.Name)
		host.Spec.Image = nil
		host.Spec.Online = false
		host.Spec.UserData = nil
		err = a.client.Update(ctx, host)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		return &machineapierrors.RequeueAfterError{}
	}

	waiting := false
	switch host.Status.Provisioning.State {
	case bmh.StateProvisioning, bmh.StateProvisioned, bmh.StateDeprovisioning:
		// Host is still provisioned or being deprovisioned.
		waiting = true
	case bmh.StateExternallyProvisioned:
		// We have no control over provisioning, so just wait until the
		// host is powered off
		waiting = host.Status.PoweredOn
	}
	if waiting {
		log.Printf("waiting for host %v to be deprovisioned", host.Name)
		return &machineapierrors.RequeueAfterError{RequeueAfter: requeueAfter}
	}

	log.Printf("clearing consumer reference for host %v", host.Name)
	host.Spec.ConsumerRef = nil
	if utils.StringInList(host.Finalizers, machinev1beta1.MachineFinalizer) {
		log.Printf("clearing machine finalizer for host %v", host.Name)
		host.Finalizers = utils.FilterStringFromList(
			host.Finalizers, machinev1beta1.MachineFinalizer)
	}
	err = a.client.Update(ctx, host)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	return nil
}

// Update updates a machine and is invoked by the Machine Controller
func (a *Actuator) Update(ctx context.Context, machine *machinev1beta1.Machine) error {
	log.Printf("Updating machine %v .", machine.Name)

	// clear any error message that was previously set. This method doesn't set
	// error messages yet, so we know that it's incorrect to have one here.
	err := a.clearError(ctx, machine)
	if err != nil {
		return err
	}

	host, err := a.getHost(ctx, machine)
	if err != nil {
		return err
	}
	if host == nil {
		// When finalizer for BHM is removed but an error occur before
		// the Machine is deleted below in the StateDeleting block, the
		// Machine should be deleted when no host is found.
		log.Print("Deleting machine whose associated host is gone: ", machine.Name)
		err = a.client.Delete(ctx, machine)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		log.Print("Deleted machine whose associated host is gone: ", machine.Name)
		return fmt.Errorf("host not found for machine %s", machine.Name)
	}

	// Delete Machine when BareMetalHost is deleted.
	// This is to ensure the MachineSet creates a new Machine
	// containing the latest ProviderSpec.
	if host.Status.Provisioning.State == bmh.StateDeleting {
		if utils.StringInList(host.Finalizers, machinev1beta1.MachineFinalizer) {
			log.Print("Removing finalizer for host: ", host.Name)
			host.Finalizers = utils.FilterStringFromList(
				host.Finalizers, machinev1beta1.MachineFinalizer)
			err = a.client.Update(ctx, host)
			if err != nil && !errors.IsNotFound(err) {
				return err
			}
			log.Print("Removed finalizer for host: ", host.Name)
		}

		log.Print("Deleting machine whose associated host is gone: ", machine.Name)
		err = a.client.Delete(ctx, machine)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		log.Print("Deleted machine whose associated host is gone: ", machine.Name)
		return nil
	}

	dirty, err := a.ensureAnnotation(ctx, machine, host)
	if err != nil {
		return err
	}

	if err := a.handleNodeFinalizer(ctx, machine); err != nil {
		return err
	}

	if !dirty {
		if err := a.remediateIfNeeded(ctx, machine, host); err != nil {
			return err
		}
	}

	if err := a.updateMachineStatus(ctx, machine, host); err != nil {
		return err
	}

	err = a.ensureProviderID(ctx, machine, host)
	if err != nil {
		return err
	}

	log.Printf("Finished updating machine %v .", machine.Name)
	return nil
}

// Exists tests for the existence of a machine and is invoked by the Machine Controller
func (a *Actuator) Exists(ctx context.Context, machine *machinev1beta1.Machine) (bool, error) {
	log.Printf("Checking if machine %v exists.", machine.Name)
	host, err := a.getHost(ctx, machine)
	if err != nil {
		return false, err
	}
	if host == nil {
		log.Printf("Machine %v does not exist.", machine.Name)
		return false, nil
	}
	log.Printf("Machine %v exists.", machine.Name)
	return true, nil
}

// The Machine Actuator interface must implement GetIP and GetKubeConfig functions as a workaround for issues
// cluster-api#158 (https://github.com/kubernetes-sigs/cluster-api/issues/158) and cluster-api#160
// (https://github.com/kubernetes-sigs/cluster-api/issues/160).

// GetIP returns IP address of the machine in the cluster.
func (a *Actuator) GetIP(machine *machinev1beta1.Machine) (string, error) {
	log.Printf("Getting IP of machine %v .", machine.Name)
	return "", fmt.Errorf("TODO: Not yet implemented")
}

// GetKubeConfig gets a kubeconfig from the running control plane.
func (a *Actuator) GetKubeConfig(controlPlaneMachine *machinev1beta1.Machine) (string, error) {
	log.Printf("Getting IP of machine %v .", controlPlaneMachine.Name)
	return "", fmt.Errorf("TODO: Not yet implemented")
}

// getHost gets the associated host by looking for an annotation on the machine
// that contains a reference to the host. Returns nil if not found. Assumes the
// host is in the same namespace as the machine.
func (a *Actuator) getHost(ctx context.Context, machine *machinev1beta1.Machine) (*bmh.BareMetalHost, error) {
	annotations := machine.ObjectMeta.GetAnnotations()
	if annotations == nil {
		return nil, nil
	}
	hostKey, ok := annotations[HostAnnotation]
	if !ok {
		return nil, nil
	}
	hostNamespace, hostName, err := cache.SplitMetaNamespaceKey(hostKey)
	if err != nil {
		log.Printf("Error parsing annotation value \"%s\": %v", hostKey, err)
		return nil, err
	}

	host := bmh.BareMetalHost{}
	key := client.ObjectKey{
		Name:      hostName,
		Namespace: hostNamespace,
	}
	err = a.client.Get(ctx, key, &host)
	if errors.IsNotFound(err) {
		log.Printf("Annotated host %s not found", hostKey)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &host, nil
}

// SelectorFromProviderSpec returns a selector that can be used to determine if
// a BareMetalHost matches a Machine.
func SelectorFromProviderSpec(providerspec *machinev1beta1.ProviderSpec) (labels.Selector, error) {
	config, err := configFromProviderSpec(*providerspec)
	if err != nil {
		log.Printf("Error reading ProviderSpec: %s", err.Error())
		return nil, err
	}
	selector := labels.NewSelector()
	var reqs labels.Requirements
	for labelKey, labelVal := range config.HostSelector.MatchLabels {
		r, err := labels.NewRequirement(labelKey, selection.Equals, []string{labelVal})
		if err != nil {
			log.Printf("Failed to create MatchLabel requirement: %v", err)
			return nil, err
		}
		reqs = append(reqs, *r)
	}
	for _, req := range config.HostSelector.MatchExpressions {
		lowercaseOperator := selection.Operator(strings.ToLower(string(req.Operator)))
		r, err := labels.NewRequirement(req.Key, lowercaseOperator, req.Values)
		if err != nil {
			log.Printf("Failed to create MatchExpression requirement: %v", err)
			return nil, err
		}
		reqs = append(reqs, *r)
	}
	selector = selector.Add(reqs...)
	return selector, nil
}

// chooseHost iterates through known hosts and returns one that can be
// associated with the machine. It searches all hosts in case one already has an
// association with this machine.
func (a *Actuator) chooseHost(ctx context.Context, machine *machinev1beta1.Machine) (*bmh.BareMetalHost, error) {
	// get list of BMH
	hosts := bmh.BareMetalHostList{}
	opts := &client.ListOptions{
		Namespace: machine.Namespace,
	}

	err := a.client.List(ctx, &hosts, opts)
	if err != nil {
		return nil, err
	}

	// Using the label selector on ListOptions above doesn't seem to work.
	// I think it's because we have a local cache of all BareMetalHosts.
	labelSelector, err := SelectorFromProviderSpec(&machine.Spec.ProviderSpec)
	if err != nil {
		return nil, err
	}

	availableHosts := []*bmh.BareMetalHost{}
	for i, host := range hosts.Items {

		if consumerRefMatches(host.Spec.ConsumerRef, machine) {
			// if we see a host that thinks this machine is consuming
			// it, we should oblige it
			log.Printf("found host %s with existing ConsumerRef", host.Name)
			return &hosts.Items[i], nil
		}

		if host.Spec.ConsumerRef != nil {
			// consumerRef is set and does not match, so the host is
			// in use
			continue
		}
		if host.GetDeletionTimestamp() != nil {
			// the host is being deleted
			continue
		}
		switch host.Status.Provisioning.State {
		case bmh.StateReady, bmh.StateAvailable:
			// the host is available to be provisioned
		default:
			// the host has not completed introspection or has an error
			continue
		}
		if host.Status.ErrorMessage != "" {
			// the host has some sort of error
			continue
		}
		if host.Spec.ExternallyProvisioned {
			// the host was provisioned by something else, we should
			// not overwrite it
			continue
		}

		if labelSelector.Matches(labels.Set(host.ObjectMeta.Labels)) {
			log.Printf("Host '%s' matched hostSelector for Machine '%s'",
				host.Name, machine.Name)
			availableHosts = append(availableHosts, &hosts.Items[i])
		} else {
			log.Printf("Host '%s' did not match hostSelector for Machine '%s'",
				host.Name, machine.Name)
		}
	}
	log.Printf("%d hosts available while choosing host for machine '%s'", len(availableHosts), machine.Name)
	if len(availableHosts) == 0 {
		return nil, nil
	}

	// choose a host at random from available hosts
	rand.Seed(time.Now().Unix())
	chosenHost := availableHosts[rand.Intn(len(availableHosts))]

	return chosenHost, nil
}

// consumerRefMatches returns a boolean based on whether the consumer
// reference and machine metadata match
func consumerRefMatches(consumer *corev1.ObjectReference, machine *machinev1beta1.Machine) bool {
	if consumer == nil {
		return false
	}
	if consumer.Name != machine.Name {
		return false
	}
	if consumer.Namespace != machine.Namespace {
		return false
	}
	if consumer.Kind != machine.Kind {
		return false
	}
	if consumer.APIVersion != machine.APIVersion {
		return false
	}
	return true
}

// setHostSpec will ensure the host's Spec is set according to the machine's
// details. It will then update the host via the kube API. If UserData does not
// include a Namespace, it will default to the Machine's namespace.
func (a *Actuator) setHostSpec(ctx context.Context, host *bmh.BareMetalHost, machine *machinev1beta1.Machine,
	config *bmv1alpha1.BareMetalMachineProviderSpec) error {

	// We only want to update the image setting if the host does not
	// already have an image.
	//
	// A host with an existing image is already provisioned and
	// upgrades are not supported at this time. To re-provision a
	// host, we must fully deprovision it and then provision it again.
	if host.Spec.Image == nil {
		host.Spec.Image = &bmh.Image{
			URL:      config.Image.URL,
			Checksum: config.Image.Checksum,
		}
		host.Spec.UserData = config.UserData
		if host.Spec.UserData != nil && host.Spec.UserData.Namespace == "" {
			host.Spec.UserData.Namespace = machine.Namespace
		}
	}

	host.Spec.ConsumerRef = &corev1.ObjectReference{
		Kind:       "Machine",
		Name:       machine.Name,
		Namespace:  machine.Namespace,
		APIVersion: machine.APIVersion,
	}

	host.Spec.Online = true
	return a.client.Update(ctx, host)
}

// ensureAnnotation makes sure the machine has an annotation that references the
// host and uses the API to update the machine if necessary.
// it returns a boolean to indicate if the machine object was changed
func (a *Actuator) ensureAnnotation(ctx context.Context, machine *machinev1beta1.Machine, host *bmh.BareMetalHost) (bool, error) {
	annotations := machine.ObjectMeta.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	hostKey, err := cache.MetaNamespaceKeyFunc(host)
	if err != nil {
		log.Printf("Error parsing annotation value \"%s\": %v", hostKey, err)
		return false, err
	}
	existing, ok := annotations[HostAnnotation]
	if ok {
		if existing == hostKey {
			return false, nil
		}
		log.Printf("Warning: found stray annotation for host %s on machine %s. Overwriting.", existing, machine.Name)
	}

	log.Printf("setting host annotation for %v to %v=%q", machine.Name, HostAnnotation, hostKey)
	annotations[HostAnnotation] = hostKey
	machine.ObjectMeta.SetAnnotations(annotations)
	err = a.client.Update(ctx, machine)
	if err != nil {
		return false, gherrors.Wrap(err, "failed to update machine annotation")
	}
	return true, nil
}

// clearAnnotation makes sure the machine's host annotation is empty.
// it returns a boolean to indicate if the machine object was changed
func (a *Actuator) clearAnnotation(ctx context.Context, machine *machinev1beta1.Machine) (bool, error) {

	annotations := machine.ObjectMeta.GetAnnotations()
	if annotations == nil {
		return false, nil
	}

	_, ok := annotations[HostAnnotation]
	if !ok {
		return false, nil
	}

	log.Printf("clearing host annotation for %v", machine.Name)
	annotations[HostAnnotation] = ""
	machine.ObjectMeta.SetAnnotations(annotations)
	return true, a.client.Update(ctx, machine)
}

// ensureProviderID adds the providerID to the Machine spec, if it does
// not already exist.  Also sets the corresponding ID on the related
// Node when it becomes referenced via the Status.NodeRef
func (a *Actuator) ensureProviderID(ctx context.Context, machine *machinev1beta1.Machine, host *bmh.BareMetalHost) error {
	providerID := "baremetalhost:///" + host.Namespace + "/" + host.Name
	existingProviderID := machine.Spec.ProviderID
	if existingProviderID == nil || *existingProviderID != providerID {
		log.Printf("Setting ProviderID %s for machine %s.", providerID, machine.Name)
		machine.Spec.ProviderID = &providerID
		err := a.client.Update(ctx, machine)
		if err != nil {
			log.Printf("Failed to update machine ProviderID, error: %s", err.Error())
			return err
		}
	}

	node, err := a.getNodeByMachine(ctx, machine)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("Not setting Node ProviderID - Node does not yet exist for Machine %s", machine.Name)
			return nil
		}
		log.Printf("Failed to get Node for ProviderID, error: %s", err.Error())
		return err
	}

	log.Printf("Setting ProviderID %s for node %s.", providerID, node.Name)
	node.Spec.ProviderID = providerID
	err = a.client.Update(ctx, node)
	if err != nil {
		log.Printf("Failed to update node ProviderID, error: %s", err.Error())
		return err
	}

	return nil
}

// handleNodeFinalizer adds finalizer to Node if not already exists
// it also store the node annotations and labels on Machine annotations
// upon node deletion
func (a *Actuator) handleNodeFinalizer(ctx context.Context, machine *machinev1beta1.Machine) error {
	node, err := a.getNodeByMachine(ctx, machine)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		log.Printf("Failed to find node associated with Machine %s, error: %s", machine.Name, err.Error())
		return err
	}

	if node.DeletionTimestamp.IsZero() {
		if !utils.StringInList(node.Finalizers, nodeFinalizer) {
			//add finalizer
			node.Finalizers = append(node.Finalizers, nodeFinalizer)
			if err := a.client.Update(ctx, node); err != nil {
				log.Printf("Failed to add node finalizer to node %s, error: %s", node.Name, err.Error())
				return err
			}
		}
	} else {
		//node is in deletion process
		if utils.StringInList(node.Finalizers, nodeFinalizer) {
			if err := a.storeAnnotationsAndLabels(ctx, node, machine); err != nil {
				return err
			}

			//remove finalizer
			node.Finalizers = utils.FilterStringFromList(node.Finalizers, nodeFinalizer)
			if err := a.client.Update(ctx, node); err != nil {
				log.Printf("Failed to remove node finalizer from %s, error: %s", node.Name, err.Error())
				return err
			}
		}
	}
	return nil
}

// removeNodeFinalizer removes the finalizer from the Node
func (a *Actuator) removeNodeFinalizer(ctx context.Context, machine *machinev1beta1.Machine) error {
	node, err := a.getNodeByMachine(ctx, machine)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		log.Printf("Failed to find node associated with Machine %s, error: %s", machine.Name, err.Error())
		return err
	}

	if utils.StringInList(node.Finalizers, nodeFinalizer) {
		node.Finalizers = utils.FilterStringFromList(node.Finalizers, nodeFinalizer)
		if err := a.client.Update(ctx, node); err != nil {
			log.Printf("Failed to remove Node finalizer from %s, error: %s", node.Name, err.Error())
			return err
		}
	}

	return nil
}

// setError sets the ErrorMessage and ErrorReason fields on the machine and logs
// the message. It assumes the reason is invalid configuration, since that is
// currently the only relevant MachineStatusError choice.
func (a *Actuator) setError(ctx context.Context, machine *machinev1beta1.Machine, message string) error {
	machine.Status.ErrorMessage = &message
	reason := machinev1beta1.InvalidConfigurationMachineError
	machine.Status.ErrorReason = &reason
	log.Printf("Machine %s: %s", machine.Name, message)
	return a.client.Status().Update(ctx, machine)
}

// clearError removes the ErrorMessage from the machine's Status if set. Returns
// nil if ErrorMessage was already nil. Returns a RequeueAfterError if the
// machine was updated.
func (a *Actuator) clearError(ctx context.Context, machine *machinev1beta1.Machine) error {
	if machine.Status.ErrorMessage != nil || machine.Status.ErrorReason != nil {
		machine.Status.ErrorMessage = nil
		machine.Status.ErrorReason = nil
		err := a.client.Status().Update(ctx, machine)
		if err != nil {
			return err
		}
		log.Printf("Cleared error message from machine %s", machine.Name)
		return &machineapierrors.RequeueAfterError{}
	}
	return nil
}

// configFromProviderSpec returns a BareMetalMachineProviderSpec by
// deserializing the contents of a ProviderSpec
func configFromProviderSpec(providerSpec machinev1beta1.ProviderSpec) (*bmv1alpha1.BareMetalMachineProviderSpec, error) {
	if providerSpec.Value == nil {
		return nil, fmt.Errorf("ProviderSpec missing")
	}

	var config bmv1alpha1.BareMetalMachineProviderSpec
	err := yaml.UnmarshalStrict(providerSpec.Value.Raw, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// updateMachineStatus updates a machine object's status.
func (a *Actuator) updateMachineStatus(ctx context.Context, machine *machinev1beta1.Machine, host *bmh.BareMetalHost) error {
	addrs, err := a.nodeAddresses(host)
	if err != nil {
		return err
	}

	if err := a.applyMachineStatus(ctx, machine, addrs); err != nil {
		return err
	}

	return nil
}

func (a *Actuator) applyMachineStatus(ctx context.Context, machine *machinev1beta1.Machine, addrs []corev1.NodeAddress) error {
	machineCopy := machine.DeepCopy()
	machineCopy.Status.Addresses = addrs

	if equality.Semantic.DeepEqual(machine.Status, machineCopy.Status) {
		// Status did not change
		return nil
	}

	now := metav1.Now()
	machineCopy.Status.LastUpdated = &now

	err := a.client.Status().Update(ctx, machineCopy)
	return err
}

// NodeAddresses returns a slice of corev1.NodeAddress objects for a
// given Baremetal machine.
func (a *Actuator) nodeAddresses(host *bmh.BareMetalHost) ([]corev1.NodeAddress, error) {
	addrs := []corev1.NodeAddress{}

	// If the host is nil or we have no hw details, return an empty address array.
	if host == nil || host.Status.HardwareDetails == nil {
		return addrs, nil
	}

	for _, nic := range host.Status.HardwareDetails.NIC {
		address := corev1.NodeAddress{
			Type:    corev1.NodeInternalIP,
			Address: nic.IP,
		}
		addrs = append(addrs, address)
	}

	if host.Status.HardwareDetails.Hostname != "" {
		addrs = append(addrs, corev1.NodeAddress{
			Type:    corev1.NodeHostName,
			Address: host.Status.HardwareDetails.Hostname,
		})
		addrs = append(addrs, corev1.NodeAddress{
			Type:    corev1.NodeInternalDNS,
			Address: host.Status.HardwareDetails.Hostname,
		})
	}

	return addrs, nil
}

//deleteRemediationAnnotations deletes poweredOffForRemediation and remediation strategy annotations
func (a *Actuator) deleteRemediationAnnotations(ctx context.Context, machine *machinev1beta1.Machine) error {
	if len(machine.Annotations) == 0 {
		return nil
	}

	delete(machine.Annotations, poweredOffForRemediation)
	delete(machine.Annotations, externalRemediationAnnotation)

	if err := a.client.Update(ctx, machine); err != nil {
		log.Printf("Failed to delete annotations of Machine: %s", machine.Name)
		return err
	}

	return nil
}

//hasPowerOffRequestAnnotation checks if the requestPowerOffAnnotation exists on the baremetalhost
func hasPowerOffRequestAnnotation(baremetalhost *bmh.BareMetalHost) (exists bool) {
	if len(baremetalhost.Annotations) > 0 {
		_, exists = baremetalhost.Annotations[requestPowerOffAnnotation]
	}
	return
}

//addPoweredOffForRemediationAnnotation adds a powered-off-for-remediation annotation to the machine
func (a *Actuator) addPoweredOffForRemediationAnnotation(ctx context.Context, machine *machinev1beta1.Machine) error {
	if machine.Annotations == nil {
		machine.Annotations = make(map[string]string)
	}

	machine.Annotations[poweredOffForRemediation] = ""

	err := a.client.Update(ctx, machine)
	if err != nil {
		log.Printf("Failed to add remediation in progess annotation to %s: %s", machine.Name, err.Error())
	}

	return err
}

//requestPowerOff adds requestPowerOffAnnotation on baremetalhost which signals BMO to power off the machine
func (a *Actuator) requestPowerOff(ctx context.Context, baremetalhost *bmh.BareMetalHost) error {
	if baremetalhost.Annotations == nil {
		baremetalhost.Annotations = make(map[string]string)
	}

	if _, powerOffRequestExists := baremetalhost.Annotations[requestPowerOffAnnotation]; powerOffRequestExists {
		return &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 5}
	}

	baremetalhost.Annotations[requestPowerOffAnnotation] = ""

	err := a.client.Update(ctx, baremetalhost)
	if err != nil {
		log.Printf("failed to add power off request annotation to %s: %s", baremetalhost.Name, err.Error())
	}

	return err
}

//requestPowerOn removes requestPowerOffAnnotation from baremetalhost which signals BMO to power on the machine
func (a *Actuator) requestPowerOn(ctx context.Context, baremetalhost *bmh.BareMetalHost) error {
	if baremetalhost.Annotations == nil {
		baremetalhost.Annotations = make(map[string]string)
	}

	if _, powerOffRequestExists := baremetalhost.Annotations[requestPowerOffAnnotation]; !powerOffRequestExists {
		return &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 5}
	}

	delete(baremetalhost.Annotations, requestPowerOffAnnotation)

	err := a.client.Update(ctx, baremetalhost)
	if err != nil {
		log.Printf("failed to power-off request annotation from %s: %s", baremetalhost.Name, err.Error())
	}

	return err
}

// deleteMachineNode deletes the node that mapped to specified machine
func (a *Actuator) deleteNode(ctx context.Context, node *corev1.Node) error {
	if !node.DeletionTimestamp.IsZero() {
		return &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 2}
	}

	err := a.client.Delete(ctx, node)
	if err != nil {
		if errors.IsNotFound(err) {
			return &machineapierrors.RequeueAfterError{}
		}
		log.Printf("Failed to delete node %s: %s", node.Name, err.Error())
		return err
	}
	return &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 2}
}

// getNodeByMachine returns the node object referenced by machine
func (a *Actuator) getNodeByMachine(ctx context.Context, machine *machinev1beta1.Machine) (*corev1.Node, error) {
	if machine.Status.NodeRef == nil {
		return nil, errors.NewNotFound(corev1.Resource("ObjectReference"), machine.Name)
	}

	node := &corev1.Node{}
	key := client.ObjectKey{
		Name:      machine.Status.NodeRef.Name,
		Namespace: machine.Status.NodeRef.Namespace,
	}

	if err := a.client.Get(ctx, key, node); err != nil {
		return nil, err
	}
	return node, nil
}

/*
remediateIfNeeded will try to remediate unhealthy machines (annotated by MHC) by power-cycle the host
The full remediation flow is:
1) Power off the host
2) Add poweredOffForRemediation annotation to the unhealthy Machine
3) Delete the node
4) Power on the host
5) Wait for the node the come up (by waiting for the node to be registered in the cluster)
6) Restore node's annotations and labels
7) Remove poweredOffForRemediation annotation, MAO's machine unhealthy annotation and annotations/labels backup
*/
func (a *Actuator) remediateIfNeeded(ctx context.Context, machine *machinev1beta1.Machine, baremetalhost *bmh.BareMetalHost) error {
	if len(machine.Annotations) == 0 {
		return nil
	}

	if _, needsRemediation := machine.Annotations[externalRemediationAnnotation]; !needsRemediation {
		return nil
	}

	if _, poweredOffForRemediation := machine.Annotations[poweredOffForRemediation]; !poweredOffForRemediation {
		if !hasPowerOffRequestAnnotation(baremetalhost) {
			log.Printf("Found an unhealthy machine, requesting power off. Machine name: %s", machine.Name)
			return a.requestPowerOff(ctx, baremetalhost)
		}

		//hold remediation until the power off request is fulfilled
		if baremetalhost.Status.PoweredOn {
			return nil
		}

		//we need this annotation to differentiate between unhealthy machine that
		//needs remediation, and an unhealthy machine that just got remediated
		return a.addPoweredOffForRemediationAnnotation(ctx, machine)
	}

	node, err := a.getNodeByMachine(ctx, machine)

	if err != nil {
		if !errors.IsNotFound(err) {
			log.Printf("Failed to get Node from Machine %s: %s", machine.Name, err.Error())
			return err
		}
	}

	if !baremetalhost.Status.PoweredOn {
		if node != nil {
			log.Printf("Deleting Node %s associated with Machine %s", node.Name, machine.Name)
			/*
				Delete the node only after the host is powered off. Otherwise, if we would delete the node
				when the host is powered on, the scheduler would assign the workload to other nodes, with the
				possibility that two instances of the same application are running in parallel. This might result
				in corruption or other issues for applications with singleton requirement. After the host is powered
				off we know for sure that it is safe to re-assign that workload to other nodes.
			*/
			return a.deleteNode(ctx, node)
		}

		// node is deleted, we can power on the host
		log.Printf("Requesting Host %s power on for Machine %s",
			baremetalhost.Name, machine.Name)
		return a.requestPowerOn(ctx, baremetalhost)
	}

	//node is still not running, so we requeue
	if node == nil {
		return &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 5}
	}

	// node is now available again - restore annotations and labels
	if _, annotationsBackupExists := machine.Annotations[nodeAnnotationsBackupAnnotation]; annotationsBackupExists {
		return a.restoreAnnotationsAndLabels(ctx, node, machine)
	}

	//remediation is done
	log.Printf("Node %s is available, remediation of Machine %s complete", node.Name, machine.Name)
	return a.deleteRemediationAnnotations(ctx, machine)
}

// storeAnnotationsAndLabels copies node's annotations and labels and stores them on machine's annotations
func (a *Actuator) storeAnnotationsAndLabels(ctx context.Context, node *corev1.Node, machine *machinev1beta1.Machine) error {
	marshaledAnnotations, err := marshal(node.Annotations)
	if err != nil {
		log.Printf("Failed to marshal node %s annotations associated with Machine %s: %s",
			node.Name, machine.Name, err.Error())
		//if marsahl fails we want to continue without blocking on this, as this error
		//not likely to be resolved in the next run
	}

	marshaledLabels, err := marshal(node.Labels)
	if err != nil {
		log.Printf("Failed to marshal node %s labels associated with Machine %s: %s",
			node.Name, machine.Name, err.Error())
	}

	if len(marshaledAnnotations) > 0 || len(marshaledLabels) > 0 {
		machine.Annotations[nodeLabelsBackupAnnotation] = marshaledLabels
		machine.Annotations[nodeAnnotationsBackupAnnotation] = marshaledAnnotations

		err = a.client.Update(ctx, machine)
		if err != nil {
			log.Printf("Failed to update machine with node's annotations and labels %s: %s", machine.Name, err.Error())
			return err
		}
	}
	return nil
}

// restoreAnnotationsAndLabels copies annotations and labels stored in machine's annotation to the node
// and removes the annotations used to store that data
func (a *Actuator) restoreAnnotationsAndLabels(ctx context.Context, node *corev1.Node, machine *machinev1beta1.Machine) error {
	if len(machine.Annotations) == 0 {
		return &machineapierrors.RequeueAfterError{}
	}

	nodeAnn, err := unmarshal(machine.Annotations[nodeAnnotationsBackupAnnotation])
	if err != nil {
		log.Printf("failed to unmarshal node's annotations from %s: %s", machine.Name, err.Error())
		//if unmarsahl fails we want to continue without blocking on this, as this error
		//not likely to be resolved in the next run
	}

	nodeLabels, err := unmarshal(machine.Annotations[nodeLabelsBackupAnnotation])
	if err != nil {
		log.Printf("failed to unmarshal node's labels from %s: %s", machine.Name, err.Error())
	}

	if len(nodeLabels) > 0 || len(nodeAnn) > 0 {
		node.Annotations = a.mergeMaps(node.Annotations, nodeAnn)
		node.Labels = a.mergeMaps(node.Labels, nodeLabels)

		if err := a.client.Update(ctx, node); err != nil {
			log.Printf("failed to update machine with node's annotations %s: %s", machine.Name, err.Error())
			return err
		}
	}

	delete(machine.Annotations, nodeAnnotationsBackupAnnotation)
	delete(machine.Annotations, nodeLabelsBackupAnnotation)

	err = a.client.Update(ctx, machine)
	if err != nil {
		log.Printf("failed to remove node %s annotations backup from machine %s: %s",
			node.Name, machine.Name, err.Error())
		return err
	}

	return nil
}

// mergeMaps takes entries from mapToMerge and adds them to prioritizedMap, if entry key not already
// exists in prioritizedMap. It returns the merged maps
func (a *Actuator) mergeMaps(prioritizedMap map[string]string, mapToMerge map[string]string) map[string]string {
	if prioritizedMap == nil {
		prioritizedMap = make(map[string]string)
	}

	// restore from backup only if key not exists
	for annKey, annValue := range mapToMerge {
		_, exists := prioritizedMap[annKey]
		if !exists {
			prioritizedMap[annKey] = annValue
		}
	}

	return prioritizedMap
}

// marshal is a wrapper for json.marshal() and converts its output to string
// if m is nil - an empty string will be returned
func marshal(m map[string]string) (string, error) {
	if m == nil {
		return "", nil
	}

	marshaled, err := json.Marshal(m)
	if err != nil {
		return "", err
	}

	return string(marshaled), nil
}

// unmarshal is a wrapper for json.Unmarshal() for marshaled strings that represent map[string]string
func unmarshal(marshaled string) (map[string]string, error) {
	if marshaled == "" {
		return make(map[string]string), nil
	}

	decodedValue := make(map[string]string)

	if err := json.Unmarshal([]byte(marshaled), &decodedValue); err != nil {
		return nil, err
	}

	return decodedValue, nil
}
