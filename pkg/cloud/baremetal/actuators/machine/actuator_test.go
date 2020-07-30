package machine

import (
	"context"
	"testing"
	"time"

	"reflect"

	bmoapis "github.com/metal3-io/baremetal-operator/pkg/apis"
	bmh "github.com/metal3-io/baremetal-operator/pkg/apis/metal3/v1alpha1"
	"github.com/metal3-io/baremetal-operator/pkg/utils"
	bmv1alpha1 "github.com/openshift/cluster-api-provider-baremetal/pkg/apis/baremetal/v1alpha1"
	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	machineapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

const (
	testImageURL                = "http://172.22.0.1/images/rhcos-ootpa-latest.qcow2"
	testImageChecksumURL        = "http://172.22.0.1/images/rhcos-ootpa-latest.qcow2.md5sum"
	testUserDataSecretName      = "worker-user-data"
	testUserDataSecretNamespace = "myns"
	testRemediationNamespace    = "remediationNs"
)

func TestChooseHost(t *testing.T) {
	scheme := runtime.NewScheme()
	bmoapis.AddToScheme(scheme)

	host1 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host1",
			Namespace: "myns",
		},
		Spec: bmh.BareMetalHostSpec{
			ConsumerRef: &corev1.ObjectReference{
				Name:       "someothermachine",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
		},
	}
	host2 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host2",
			Namespace: "myns",
		},
	}
	host3 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host3",
			Namespace: "myns",
		},
		Spec: bmh.BareMetalHostSpec{
			ConsumerRef: &corev1.ObjectReference{
				Name:       "machine1",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
		},
	}
	host4 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host4",
			Namespace: "someotherns",
		},
	}
	discoveredHost := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "discoveredHost",
			Namespace: "myns",
		},
		Status: bmh.BareMetalHostStatus{
			ErrorMessage: "this host is discovered and not usable",
		},
	}
	hostWithLabel := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host_with_label",
			Namespace: "myns",
			Labels:    map[string]string{"key1": "value1"},
		},
	}
	externallyProvisionedHost := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "externally-provisioned-host",
			Namespace: "myns",
		},
		Spec: bmh.BareMetalHostSpec{
			ExternallyProvisioned: true,
		},
	}
	externallyProvisionedAndConsumedHost := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "externally-provisioned-and-consumed-host",
			Namespace: "myns",
		},
		Spec: bmh.BareMetalHostSpec{
			ExternallyProvisioned: true,
			ConsumerRef: &corev1.ObjectReference{
				Name:       "machine1",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
		},
	}

	config, providerSpec := newConfig(t, "", map[string]string{}, []bmv1alpha1.HostSelectorRequirement{})
	config2, providerSpec2 := newConfig(t, "", map[string]string{"key1": "value1"}, []bmv1alpha1.HostSelectorRequirement{})
	config3, providerSpec3 := newConfig(t, "", map[string]string{"boguskey": "value"}, []bmv1alpha1.HostSelectorRequirement{})
	config4, providerSpec4 := newConfig(t, "", map[string]string{},
		[]bmv1alpha1.HostSelectorRequirement{
			bmv1alpha1.HostSelectorRequirement{
				Key:      "key1",
				Operator: "in",
				Values:   []string{"abc", "value1", "123"},
			},
		})
	config5, providerSpec5 := newConfig(t, "", map[string]string{},
		[]bmv1alpha1.HostSelectorRequirement{
			bmv1alpha1.HostSelectorRequirement{
				Key:      "key1",
				Operator: "pancakes",
				Values:   []string{"abc", "value1", "123"},
			},
		})

	testCases := []struct {
		Machine          machinev1beta1.Machine
		Hosts            []runtime.Object
		ExpectedHostName string
		Config           *bmv1alpha1.BareMetalMachineProviderSpec
	}{
		{
			// should pick host2, which lacks a ConsumerRef
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec,
				},
			},
			Hosts:            []runtime.Object{&host2, &host1},
			ExpectedHostName: host2.Name,
			Config:           config,
		},
		{
			// should ignore discoveredHost and pick host2, which lacks a ConsumerRef
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
			},
			Hosts:            []runtime.Object{&discoveredHost, &host2, &host1},
			ExpectedHostName: host2.Name,
			Config:           config,
		},
		{
			// should pick host3, which already has a matching ConsumerRef
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec,
				},
			},
			Hosts:            []runtime.Object{&host1, &host3, &host2},
			ExpectedHostName: host3.Name,
			Config:           config,
		},
		{
			// should not pick a host, because two are already taken, and the third is in
			// a different namespace
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine2",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec,
				},
			},
			Hosts:            []runtime.Object{&host1, &host3, &host4},
			ExpectedHostName: "",
			Config:           config,
		},
		{
			// Can choose hosts with a label, even without a label selector
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec,
				},
			},
			Hosts:            []runtime.Object{&hostWithLabel},
			ExpectedHostName: hostWithLabel.Name,
			Config:           config,
		},
		{
			// Choose the host with the right label
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec2,
				},
			},
			Hosts:            []runtime.Object{&host2, &hostWithLabel},
			ExpectedHostName: hostWithLabel.Name,
			Config:           config2,
		},
		{
			// No host that matches required label
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec3,
				},
			},
			Hosts:            []runtime.Object{&host2, &hostWithLabel},
			ExpectedHostName: "",
			Config:           config3,
		},
		{
			// Host that matches a matchExpression
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec4,
				},
			},
			Hosts:            []runtime.Object{&host2, &hostWithLabel},
			ExpectedHostName: hostWithLabel.Name,
			Config:           config4,
		},
		{
			// No Host available that matches a matchExpression
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec4,
				},
			},
			Hosts:            []runtime.Object{&host2},
			ExpectedHostName: "",
			Config:           config4,
		},
		{
			// No host chosen given an Invalid match expression
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec5,
				},
			},
			Hosts:            []runtime.Object{&host2, &hostWithLabel},
			ExpectedHostName: "",
			Config:           config5,
		},
		{
			// No host chosen given only externally provisioned choices
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec5,
				},
			},
			Hosts:            []runtime.Object{&externallyProvisionedHost},
			ExpectedHostName: "",
			Config:           config,
		},
		{
			// No host chosen given only externally provisioned choices
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec5,
				},
			},
			Hosts: []runtime.Object{&externallyProvisionedHost,
				&externallyProvisionedAndConsumedHost},
			ExpectedHostName: "externally-provisioned-and-consumed-host",
			Config:           config,
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme, tc.Hosts...)

		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Errorf("%v", err)
		}
		pspec, err := yaml.Marshal(&tc.Config)
		if err != nil {
			t.Logf("could not marshal BareMetalMachineProviderSpec: %v", err)
			t.FailNow()
		}
		tc.Machine.Spec.ProviderSpec = machinev1beta1.ProviderSpec{Value: &runtime.RawExtension{Raw: pspec}}
		result, err := actuator.chooseHost(context.TODO(), &tc.Machine)
		if tc.ExpectedHostName == "" {
			if result != nil {
				t.Error("found host when none should have been available")
			}
			continue
		}
		if err != nil {
			t.Errorf("%v", err)
			return
		}
		if result.Name != tc.ExpectedHostName {
			t.Errorf("host %s chosen instead of %s", result.Name, tc.ExpectedHostName)
		}
	}
}

func TestSetHostSpec(t *testing.T) {
	for _, tc := range []struct {
		Scenario                  string
		UserDataNamespace         string
		ExpectedUserDataNamespace string
		Host                      bmh.BareMetalHost
		ExpectedImage             *bmh.Image
		ExpectUserData            bool
	}{
		{
			Scenario:                  "user data has explicit alternate namespace",
			UserDataNamespace:         "otherns",
			ExpectedUserDataNamespace: "otherns",
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host2",
					Namespace: "myns",
				},
			},
			ExpectedImage: &bmh.Image{
				URL:      testImageURL,
				Checksum: testImageChecksumURL,
			},
			ExpectUserData: true,
		},

		{
			Scenario:                  "user data has no namespace",
			UserDataNamespace:         "",
			ExpectedUserDataNamespace: "myns",
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host2",
					Namespace: "myns",
				},
			},
			ExpectedImage: &bmh.Image{
				URL:      testImageURL,
				Checksum: testImageChecksumURL,
			},
			ExpectUserData: true,
		},

		{
			Scenario:                  "externally provisioned, same machine",
			UserDataNamespace:         "",
			ExpectedUserDataNamespace: "myns",
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host2",
					Namespace: "myns",
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "machine1",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
			},
			ExpectedImage: &bmh.Image{
				URL:      testImageURL,
				Checksum: testImageChecksumURL,
			},
			ExpectUserData: true,
		},

		{
			Scenario:                  "previously provisioned, different image, unchanged",
			UserDataNamespace:         "",
			ExpectedUserDataNamespace: "myns",
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host2",
					Namespace: "myns",
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "machine1",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
					Image: &bmh.Image{
						URL:      testImageURL + "test",
						Checksum: testImageChecksumURL + "test",
					},
				},
			},
			ExpectedImage: &bmh.Image{
				URL:      testImageURL + "test",
				Checksum: testImageChecksumURL + "test",
			},
			ExpectUserData: false,
		},
	} {

		t.Run(tc.Scenario, func(t *testing.T) {
			// test data
			config, providerSpec := newConfig(t, tc.UserDataNamespace, map[string]string{}, []bmv1alpha1.HostSelectorRequirement{})
			machine := machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: providerSpec,
				},
			}

			// test setup
			scheme := runtime.NewScheme()
			bmoapis.AddToScheme(scheme)
			c := fakeclient.NewFakeClientWithScheme(scheme, &tc.Host)

			actuator, err := NewActuator(ActuatorParams{
				Client: c,
			})
			if err != nil {
				t.Errorf("%v", err)
				return
			}

			// run the function
			err = actuator.setHostSpec(context.TODO(), &tc.Host, &machine, config)
			if err != nil {
				t.Errorf("%v", err)
				return
			}

			// get the saved result
			savedHost := bmh.BareMetalHost{}
			err = c.Get(context.TODO(), client.ObjectKey{Name: tc.Host.Name, Namespace: tc.Host.Namespace}, &savedHost)
			if err != nil {
				t.Errorf("%v", err)
				return
			}

			// validate the result
			if savedHost.Spec.ConsumerRef == nil {
				t.Errorf("ConsumerRef not set")
				return
			}
			if savedHost.Spec.ConsumerRef.Name != machine.Name {
				t.Errorf("found consumer ref %v", savedHost.Spec.ConsumerRef)
			}
			if savedHost.Spec.ConsumerRef.Namespace != machine.Namespace {
				t.Errorf("found consumer ref %v", savedHost.Spec.ConsumerRef)
			}
			if savedHost.Spec.ConsumerRef.Kind != "Machine" {
				t.Errorf("found consumer ref %v", savedHost.Spec.ConsumerRef)
			}
			if savedHost.Spec.Online != true {
				t.Errorf("host not set to Online")
			}
			if tc.ExpectedImage == nil {
				if savedHost.Spec.Image != nil {
					t.Errorf("Expected image %v but got %v", tc.ExpectedImage, savedHost.Spec.Image)
					return
				}
			} else {
				if *(savedHost.Spec.Image) != *(tc.ExpectedImage) {
					t.Errorf("Expected image %v but got %v", tc.ExpectedImage, savedHost.Spec.Image)
					return
				}
			}
			if tc.ExpectUserData {
				if savedHost.Spec.UserData == nil {
					t.Errorf("UserData not set")
					return
				}
				if savedHost.Spec.UserData.Namespace != tc.ExpectedUserDataNamespace {
					t.Errorf("expected Userdata.Namespace %s, got %s", tc.ExpectedUserDataNamespace, savedHost.Spec.UserData.Namespace)
				}
				if savedHost.Spec.UserData.Name != testUserDataSecretName {
					t.Errorf("expected Userdata.Name %s, got %s", testUserDataSecretName, savedHost.Spec.UserData.Name)
				}
			} else {
				if savedHost.Spec.UserData != nil {
					t.Errorf("did not expect user data, got %v", savedHost.Spec.UserData)
				}
			}
		})
	}
}

func TestExists(t *testing.T) {
	scheme := runtime.NewScheme()
	bmoapis.AddToScheme(scheme)

	host := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "somehost",
			Namespace: "myns",
		},
	}
	c := fakeclient.NewFakeClientWithScheme(scheme, &host)

	testCases := []struct {
		Client      client.Client
		Machine     machinev1beta1.Machine
		Expected    bool
		FailMessage string
	}{
		{
			Client: c,
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						HostAnnotation: "myns/somehost",
					},
				},
			},
			Expected:    true,
			FailMessage: "failed to find the existing host",
		},
		{
			Client: c,
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						HostAnnotation: "myns/wrong",
					},
				},
			},
			Expected:    false,
			FailMessage: "found host even though annotation value incorrect",
		},
		{
			Client:      c,
			Machine:     machinev1beta1.Machine{},
			Expected:    false,
			FailMessage: "found host even though annotation not present",
		},
	}

	for _, tc := range testCases {
		actuator, err := NewActuator(ActuatorParams{
			Client: tc.Client,
		})
		if err != nil {
			t.Error(err)
		}

		result, err := actuator.Exists(context.TODO(), &tc.Machine)
		if err != nil {
			t.Error(err)
		}
		if result != tc.Expected {
			t.Error(tc.FailMessage)
		}
	}
}
func TestGetHost(t *testing.T) {
	scheme := runtime.NewScheme()
	bmoapis.AddToScheme(scheme)

	host := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myhost",
			Namespace: "myns",
		},
	}
	c := fakeclient.NewFakeClientWithScheme(scheme, &host)

	testCases := []struct {
		Client        client.Client
		Machine       machinev1beta1.Machine
		ExpectPresent bool
		FailMessage   string
	}{
		{
			Client: c,
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectPresent: true,
			FailMessage:   "did not find expected host",
		},
		{
			Client: c,
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						HostAnnotation: "myns/wrong",
					},
				},
			},
			ExpectPresent: false,
			FailMessage:   "found host even though annotation value incorrect",
		},
		{
			Client:        c,
			Machine:       machinev1beta1.Machine{},
			ExpectPresent: false,
			FailMessage:   "found host even though annotation not present",
		},
	}

	for _, tc := range testCases {
		actuator, err := NewActuator(ActuatorParams{
			Client: tc.Client,
		})
		if err != nil {
			t.Error(err)
		}

		result, err := actuator.getHost(context.TODO(), &tc.Machine)
		if err != nil {
			t.Error(err)
		}
		if (result != nil) != tc.ExpectPresent {
			t.Error(tc.FailMessage)
		}
	}
}

func TestEnsureProviderID(t *testing.T) {
	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)
	bmoapis.AddToScheme(scheme)
	corev1.AddToScheme(scheme)

	c := fakeclient.NewFakeClientWithScheme(scheme)

	testCases := []struct {
		Machine machinev1beta1.Machine
		Host    bmh.BareMetalHost
		Node    corev1.Node
	}{
		{
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
				},
			},
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
				},
			},
		},
		{
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine2",
					Namespace: "myns2",
				},
				Status: machinev1beta1.MachineStatus{
					NodeRef: &corev1.ObjectReference{
						Kind:      "Node",
						Namespace: "myns2",
						Name:      "mynode2",
					},
				},
			},
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost2",
					Namespace: "myns2",
				},
			},
			Node: corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mynode2",
					Namespace: "myns2",
				},
			},
		},
	}

	for _, tc := range testCases {
		c.Create(context.TODO(), &tc.Machine)
		c.Create(context.TODO(), &tc.Host)
		if tc.Node.Name != "" {
			c.Create(context.TODO(), &tc.Node)
		}
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		err = actuator.ensureProviderID(context.TODO(), &tc.Machine, &tc.Host)
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}

		// get the machine and make sure it has the correct ProviderID
		machine := machinev1beta1.Machine{}
		key := client.ObjectKey{
			Name:      tc.Machine.Name,
			Namespace: tc.Machine.Namespace,
		}
		err = c.Get(context.TODO(), key, &machine)
		providerID := machine.Spec.ProviderID
		if providerID == nil {
			t.Error("no machine.Spec.ProviderID found")
		}
		expectedProviderID := "baremetalhost:///" + tc.Host.Namespace + "/" + tc.Host.Name
		if *providerID != expectedProviderID {
			t.Errorf("ProviderID has value %s, expected %s",
				*providerID, expectedProviderID)
		}
		if tc.Node.Name != "" {
			node := &corev1.Node{}
			nodeKey := client.ObjectKey{
				Name:      tc.Node.Name,
				Namespace: tc.Node.Namespace,
			}
			err = c.Get(context.TODO(), nodeKey, node)
			nodeProviderID := node.Spec.ProviderID
			if nodeProviderID != expectedProviderID {
				t.Errorf("Node ProviderID has value %s, expected %s",
					nodeProviderID, expectedProviderID)
			}
		}
	}
}

func TestEnsureAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)

	testCases := []struct {
		Machine machinev1beta1.Machine
		Host    bmh.BareMetalHost
	}{
		{
			// annotation exists and is correct
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
				},
			},
		},
		{
			// annotation exists but is wrong
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						HostAnnotation: "myns/wrongvalue",
					},
				},
			},
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
				},
			},
		},
		{
			// annotations are empty
			Machine: machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
				},
			},
		},
		{
			// annotations are nil
			Machine: machinev1beta1.Machine{},
			Host: bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
				},
			},
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme, &tc.Machine)
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		_, err = actuator.ensureAnnotation(context.TODO(), &tc.Machine, &tc.Host)
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}

		// get the machine and make sure it has the correct annotation
		machine := machinev1beta1.Machine{}
		key := client.ObjectKey{
			Name:      tc.Machine.Name,
			Namespace: tc.Machine.Namespace,
		}
		err = c.Get(context.TODO(), key, &machine)
		annotations := machine.ObjectMeta.GetAnnotations()
		if annotations == nil {
			t.Error("no annotations found")
		}
		result, ok := annotations[HostAnnotation]
		if !ok {
			t.Error("host annotation not found")
		}
		if result != "myns/myhost" {
			t.Errorf("host annotation has value %s, expected \"myns/myhost\"", result)
		}
	}
}

func TestDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	bmoapis.AddToScheme(scheme)
	machinev1beta1.AddToScheme(scheme)

	testCases := []struct {
		CaseName            string
		Host                *bmh.BareMetalHost
		Machine             machinev1beta1.Machine
		ExpectedConsumerRef *corev1.ObjectReference
		ExpectedResult      error
		ExpectHostFinalizer bool
	}{
		{
			CaseName: "deprovisioning required",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
					Image: &bmh.Image{
						URL: "myimage",
					},
				},
				Status: bmh.BareMetalHostStatus{
					Provisioning: bmh.ProvisionStatus{
						State: bmh.StateProvisioned,
					},
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectedConsumerRef: &corev1.ObjectReference{
				Name:       "mymachine",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
			ExpectedResult:      &machineapierrors.RequeueAfterError{},
			ExpectHostFinalizer: true,
		},

		{
			CaseName: "deprovisioning in progress",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
				Status: bmh.BareMetalHostStatus{
					Provisioning: bmh.ProvisionStatus{
						State: bmh.StateDeprovisioning,
					},
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectedConsumerRef: &corev1.ObjectReference{
				Name:       "mymachine",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
			ExpectedResult:      &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 30},
			ExpectHostFinalizer: true,
		},

		{
			CaseName: "externally provisioned host should be powered down",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
				Status: bmh.BareMetalHostStatus{
					Provisioning: bmh.ProvisionStatus{
						State: bmh.StateExternallyProvisioned,
					},
					PoweredOn: true,
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectedConsumerRef: &corev1.ObjectReference{
				Name:       "mymachine",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
			ExpectedResult:      &machineapierrors.RequeueAfterError{RequeueAfter: time.Second * 30},
			ExpectHostFinalizer: true,
		},

		{
			CaseName: "consumer ref should be removed from externally provisioned host",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
				Status: bmh.BareMetalHostStatus{
					Provisioning: bmh.ProvisionStatus{
						State: bmh.StateExternallyProvisioned,
					},
					PoweredOn: false,
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectHostFinalizer: false,
		},

		{
			CaseName: "consumer ref should be removed",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
				Status: bmh.BareMetalHostStatus{
					Provisioning: bmh.ProvisionStatus{
						State: bmh.StateReady,
					},
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectHostFinalizer: false,
		},

		{
			CaseName: "consumer ref does not match, so it should not be removed",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "someoneelsesmachine",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
					Image: &bmh.Image{
						URL: "someoneelsesimage",
					},
				},
				Status: bmh.BareMetalHostStatus{
					Provisioning: bmh.ProvisionStatus{
						State: bmh.StateProvisioned,
					},
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectedConsumerRef: &corev1.ObjectReference{
				Name:       "someoneelsesmachine",
				Namespace:  "myns",
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
			},
			ExpectHostFinalizer: true,
		},

		{
			CaseName: "no consumer ref, so this is a no-op",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost",
					Namespace: "myns",
					Finalizers: []string{
						machinev1beta1.MachineFinalizer,
					},
				},
			},
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectHostFinalizer: true,
		},

		{
			CaseName: "no host at all, so this is a no-op",
			Host:     nil,
			Machine: machinev1beta1.Machine{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Machine",
					APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost",
					},
				},
			},
			ExpectHostFinalizer: false,
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme)
		if tc.Host != nil {
			c.Create(context.TODO(), tc.Host)
		}
		c.Create(context.TODO(), &(tc.Machine))
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		err = actuator.Delete(context.TODO(), &tc.Machine)

		var expectedResult bool
		switch e := tc.ExpectedResult.(type) {
		case nil:
			expectedResult = (err == nil)
		case *machineapierrors.RequeueAfterError:
			var perr *machineapierrors.RequeueAfterError
			if perr, expectedResult = err.(*machineapierrors.RequeueAfterError); expectedResult {
				expectedResult = (*e == *perr)
			}
		}
		if !expectedResult {
			t.Errorf("%s: unexpected error \"%v\" (expected \"%v\")",
				tc.CaseName, err, tc.ExpectedResult)
		}

		if tc.Host == nil {
			continue
		}

		key := client.ObjectKey{
			Name:      tc.Host.Name,
			Namespace: tc.Host.Namespace,
		}
		host := bmh.BareMetalHost{}
		c.Get(context.TODO(), key, &host)

		name := ""
		if host.Spec.ConsumerRef != nil {
			name = host.Spec.ConsumerRef.Name
		}

		expectedName := ""
		if tc.ExpectedConsumerRef != nil {
			expectedName = tc.ExpectedConsumerRef.Name
		}

		if name != expectedName {
			t.Errorf("%s: expected ConsumerRef %v, found %v",
				tc.CaseName, tc.ExpectedConsumerRef, host.Spec.ConsumerRef)
		}

		t.Logf("host finalizers %v", host.Finalizers)
		haveFinalizer := utils.StringInList(host.Finalizers, machinev1beta1.MachineFinalizer)
		if tc.ExpectHostFinalizer && !haveFinalizer {
			t.Errorf("%s: expected host to have finalizer and it does not %v",
				tc.CaseName, host.Finalizers)
		}
		if !tc.ExpectHostFinalizer && haveFinalizer {
			t.Errorf("%s: did not expect host to have finalizer and it does %v",
				tc.CaseName, host.Finalizers)
		}
	}
}

func TestConfigFromProviderSpec(t *testing.T) {
	ps := machinev1beta1.ProviderSpec{
		Value: &runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"baremetal.cluster.k8s.io/v1alpha1","userData":{"Name":"worker-user-data","Namespace":"myns"},"image":{"Checksum":"http://172.22.0.1/images/rhcos-ootpa-latest.qcow2.md5sum","URL":"http://172.22.0.1/images/rhcos-ootpa-latest.qcow2"},"kind":"BareMetalMachineProviderSpec"}`),
		},
	}
	config, err := configFromProviderSpec(ps)
	if err != nil {
		t.Errorf("Error: %s", err.Error())
		return
	}
	if config == nil {
		t.Errorf("got a nil config")
		return
	}

	if config.Image.URL != testImageURL {
		t.Errorf("expected Image.URL %s, got %s", testImageURL, config.Image.URL)
	}
	if config.Image.Checksum != testImageChecksumURL {
		t.Errorf("expected Image.Checksum %s, got %s", testImageChecksumURL, config.Image.Checksum)
	}
	if config.UserData == nil {
		t.Errorf("UserData not set")
		return
	}
	if config.UserData.Name != testUserDataSecretName {
		t.Errorf("expected UserData.Name %s, got %s", testUserDataSecretName, config.UserData.Name)
	}
	if config.UserData.Namespace != testUserDataSecretNamespace {
		t.Errorf("expected UserData.Namespace %s, got %s", testUserDataSecretNamespace, config.UserData.Namespace)
	}
}

func newConfig(t *testing.T, UserDataNamespace string, labels map[string]string, reqs []bmv1alpha1.HostSelectorRequirement) (*bmv1alpha1.BareMetalMachineProviderSpec, machinev1beta1.ProviderSpec) {
	config := bmv1alpha1.BareMetalMachineProviderSpec{
		Image: bmv1alpha1.Image{
			URL:      testImageURL,
			Checksum: testImageChecksumURL,
		},
		UserData: &corev1.SecretReference{
			Name:      testUserDataSecretName,
			Namespace: UserDataNamespace,
		},
		HostSelector: bmv1alpha1.HostSelector{
			MatchLabels:      labels,
			MatchExpressions: reqs,
		},
	}
	out, err := yaml.Marshal(&config)
	if err != nil {
		t.Logf("could not marshal BareMetalMachineProviderSpec: %v", err)
		t.FailNow()
	}
	return &config, machinev1beta1.ProviderSpec{
		Value: &runtime.RawExtension{Raw: out},
	}
}

func TestUpdateMachineStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)

	nic1 := bmh.NIC{
		IP: "192.168.1.1",
	}

	nic2 := bmh.NIC{
		IP: "172.0.20.2",
	}

	testCases := []struct {
		Host            *bmh.BareMetalHost
		Machine         *machinev1beta1.Machine
		ExpectedMachine machinev1beta1.Machine
	}{
		{
			// machine status updated
			Host: &bmh.BareMetalHost{
				Status: bmh.BareMetalHostStatus{
					HardwareDetails: &bmh.HardwareDetails{
						NIC: []bmh.NIC{nic1, nic2},
					},
				},
			},
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
				},
				Status: machinev1beta1.MachineStatus{},
			},
			ExpectedMachine: machinev1beta1.Machine{
				Status: machinev1beta1.MachineStatus{
					Addresses: []corev1.NodeAddress{
						corev1.NodeAddress{
							Address: "192.168.1.1",
							Type:    "InternalIP",
						},
						corev1.NodeAddress{
							Address: "172.0.20.2",
							Type:    "InternalIP",
						},
					},
				},
			},
		},
		{
			// machine status unchanged
			Host: &bmh.BareMetalHost{
				Status: bmh.BareMetalHostStatus{
					HardwareDetails: &bmh.HardwareDetails{
						NIC: []bmh.NIC{nic1, nic2},
					},
				},
			},
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
				},
				Status: machinev1beta1.MachineStatus{
					Addresses: []corev1.NodeAddress{
						corev1.NodeAddress{
							Address: "192.168.1.1",
							Type:    "InternalIP",
						},
						corev1.NodeAddress{
							Address: "172.0.20.2",
							Type:    "InternalIP",
						},
					},
				},
			},
			ExpectedMachine: machinev1beta1.Machine{
				Status: machinev1beta1.MachineStatus{
					Addresses: []corev1.NodeAddress{
						corev1.NodeAddress{
							Address: "192.168.1.1",
							Type:    "InternalIP",
						},
						corev1.NodeAddress{
							Address: "172.0.20.2",
							Type:    "InternalIP",
						},
					},
				},
			},
		},
		{
			// machine status unchanged
			Host: &bmh.BareMetalHost{},
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine",
					Namespace: "myns",
				},
				Status: machinev1beta1.MachineStatus{},
			},
			ExpectedMachine: machinev1beta1.Machine{
				Status: machinev1beta1.MachineStatus{},
			},
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme)
		if tc.Machine != nil {
			c.Create(context.TODO(), tc.Machine)
		}
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		err = actuator.updateMachineStatus(context.TODO(), tc.Machine, tc.Host)
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
		key := client.ObjectKey{
			Name:      tc.Machine.Name,
			Namespace: tc.Machine.Namespace,
		}
		machine := machinev1beta1.Machine{}
		c.Get(context.TODO(), key, &machine)

		if &tc.Machine != nil {
			key := client.ObjectKey{
				Name:      tc.Machine.Name,
				Namespace: tc.Machine.Namespace,
			}
			machine := machinev1beta1.Machine{}
			c.Get(context.TODO(), key, &machine)

			if tc.Machine.Status.Addresses != nil {
				for i, address := range tc.ExpectedMachine.Status.Addresses {
					if address != machine.Status.Addresses[i] {
						t.Errorf("expected Address %v, found %v", address, machine.Status.Addresses[i])
					}
				}
			}
		}
	}
}

func TestApplyMachineStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)

	addr1 := corev1.NodeAddress{
		Type:    "InternalIP",
		Address: "192.168.1.1",
	}

	addr2 := corev1.NodeAddress{
		Type:    "InternalIP",
		Address: "172.0.20.2",
	}

	testCases := []struct {
		Machine               *machinev1beta1.Machine
		Addresses             []corev1.NodeAddress
		ExpectedNodeAddresses []corev1.NodeAddress
	}{
		{
			// Machine status updated
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				Status: machinev1beta1.MachineStatus{
					Addresses: []corev1.NodeAddress{},
				},
			},
			Addresses:             []corev1.NodeAddress{addr1, addr2},
			ExpectedNodeAddresses: []corev1.NodeAddress{addr1, addr2},
		},
		{
			// Machine status unchanged
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "machine1",
					Namespace: "myns",
				},
				Status: machinev1beta1.MachineStatus{
					Addresses: []corev1.NodeAddress{addr1, addr2},
				},
			},
			Addresses:             []corev1.NodeAddress{addr1, addr2},
			ExpectedNodeAddresses: []corev1.NodeAddress{addr1, addr2},
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme)
		if tc.Machine != nil {
			c.Create(context.TODO(), tc.Machine)
		}
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		err = actuator.applyMachineStatus(context.TODO(), tc.Machine, tc.Addresses)
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}

		key := client.ObjectKey{
			Name:      tc.Machine.Name,
			Namespace: tc.Machine.Namespace,
		}
		machine := machinev1beta1.Machine{}
		c.Get(context.TODO(), key, &machine)

		if tc.Machine.Status.Addresses != nil {
			for i, address := range tc.ExpectedNodeAddresses {
				if address != machine.Status.Addresses[i] {
					t.Errorf("expected Address %v, found %v", address, machine.Status.Addresses[i])
				}
			}
		}
	}
}

func TestNodeAddresses(t *testing.T) {
	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)

	nic1 := bmh.NIC{
		IP: "192.168.1.1",
	}

	nic2 := bmh.NIC{
		IP: "172.0.20.2",
	}

	addr1 := corev1.NodeAddress{
		Type:    corev1.NodeInternalIP,
		Address: "192.168.1.1",
	}

	addr2 := corev1.NodeAddress{
		Type:    corev1.NodeInternalIP,
		Address: "172.0.20.2",
	}

	addr3 := corev1.NodeAddress{
		Type:    corev1.NodeHostName,
		Address: "mygreathost",
	}

	testCases := []struct {
		Host                  *bmh.BareMetalHost
		ExpectedNodeAddresses []corev1.NodeAddress
	}{
		{
			// One NIC
			Host: &bmh.BareMetalHost{
				Status: bmh.BareMetalHostStatus{
					HardwareDetails: &bmh.HardwareDetails{
						NIC: []bmh.NIC{nic1},
					},
				},
			},
			ExpectedNodeAddresses: []corev1.NodeAddress{addr1},
		},
		{
			// Two NICs
			Host: &bmh.BareMetalHost{
				Status: bmh.BareMetalHostStatus{
					HardwareDetails: &bmh.HardwareDetails{
						NIC: []bmh.NIC{nic1, nic2},
					},
				},
			},
			ExpectedNodeAddresses: []corev1.NodeAddress{addr1, addr2},
		},
		{
			// Hostname is set
			Host: &bmh.BareMetalHost{
				Status: bmh.BareMetalHostStatus{
					HardwareDetails: &bmh.HardwareDetails{
						Hostname: "mygreathost",
					},
				},
			},
			ExpectedNodeAddresses: []corev1.NodeAddress{addr3},
		},
		{
			// Empty Hostname
			Host: &bmh.BareMetalHost{
				Status: bmh.BareMetalHostStatus{
					HardwareDetails: &bmh.HardwareDetails{
						Hostname: "",
					},
				},
			},
			ExpectedNodeAddresses: []corev1.NodeAddress{},
		},
		{
			// no host at all, so this is a no-op
			Host:                  nil,
			ExpectedNodeAddresses: nil,
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme)
		if tc.Host != nil {
			c.Create(context.TODO(), tc.Host)
		}
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		nodeAddresses, err := actuator.nodeAddresses(tc.Host)
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
		expectedNodeAddresses := tc.ExpectedNodeAddresses

		for i, address := range expectedNodeAddresses {
			if address != nodeAddresses[i] {
				t.Errorf("expected Address %v, found %v", address, nodeAddresses[i])
			}
		}
	}
}

func TestDeleteOfBareMetalHostDeletesMachine(t *testing.T) {
	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)
	bmoapis.AddToScheme(scheme)

	testCases := []struct {
		CaseName              string
		Host                  *bmh.BareMetalHost
		Machine               *machinev1beta1.Machine
		ExpectedMachineExists bool
	}{
		{
			CaseName: "machine should not be deleted",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost1",
					Namespace: "myns",
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine1",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
				Status: bmh.BareMetalHostStatus{
					HardwareProfile: "dell",
					Provisioning: bmh.ProvisionStatus{
						State: "ready",
					},
				},
			},
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine1",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost1",
					},
				},
				Status: machinev1beta1.MachineStatus{},
			},
			ExpectedMachineExists: true,
		},
		{
			CaseName: "machine should be deleted",
			Host: &bmh.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "myhost2",
					Namespace: "myns",
				},
				Spec: bmh.BareMetalHostSpec{
					ConsumerRef: &corev1.ObjectReference{
						Name:       "mymachine2",
						Namespace:  "myns",
						Kind:       "Machine",
						APIVersion: machinev1beta1.SchemeGroupVersion.String(),
					},
				},
				Status: bmh.BareMetalHostStatus{
					HardwareProfile: "dell",
					Provisioning: bmh.ProvisionStatus{
						State: "deleting",
					},
				},
			},
			Machine: &machinev1beta1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mymachine2",
					Namespace: "myns",
					Annotations: map[string]string{
						HostAnnotation: "myns/myhost2",
					},
				},
				Status: machinev1beta1.MachineStatus{},
			},
			ExpectedMachineExists: false,
		},
	}

	for _, tc := range testCases {
		c := fakeclient.NewFakeClientWithScheme(scheme)
		if tc.Machine != nil {
			c.Create(context.TODO(), tc.Machine)
		}
		if tc.Host != nil {
			c.Create(context.TODO(), tc.Host)
		}
		actuator, err := NewActuator(ActuatorParams{
			Client: c,
		})
		if err != nil {
			t.Error(err)
		}

		err = actuator.Update(context.TODO(), tc.Machine)
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}

		if &tc.Machine != nil {
			key := client.ObjectKey{
				Name:      tc.Machine.Name,
				Namespace: tc.Machine.Namespace,
			}
			machine := machinev1beta1.Machine{}

			err = c.Get(context.TODO(), key, &machine)
			if tc.ExpectedMachineExists == false {
				if !errors.IsNotFound(err) {
					t.Errorf("Expected machine to not exist but received error: %v", err)
				}
			} else {
				result, err := actuator.Exists(context.TODO(), tc.Machine)
				if err != nil {
					t.Error(err)
				}
				if tc.ExpectedMachineExists != result {
					t.Errorf("ExpectedMachineExists: %v, found %v", tc.ExpectedMachineExists, result)
				}
			}
		}
	}
}

func TestMarshalAndUnmarshal(t *testing.T) {
	m := map[string]string{"key1": "val1", "keyWithEmptyValue": "", "key.with.dots": "value.with.dots"}

	marshaled, err := marshal(m)

	if err != nil {
		t.Errorf("marshal() returned an err: %s", err.Error())
	}

	unmarshaled, err := unmarshal(marshaled)

	if err != nil {
		t.Errorf("unmarshal returned an err: %s", err.Error())
	}

	if !reflect.DeepEqual(unmarshaled, m) {
		t.Errorf("map is different after unmarshal(marshal(map)). Original map was: %s , unmarshaled map: %s",
			m, unmarshaled)
	}

	m = nil

	marshaled, err = marshal(m)

	if err != nil {
		t.Errorf("marshal() returned an err: %s", err.Error())
	}

	if marshaled != "" {
		t.Errorf("Expected marshal(nil) to return an empty string, but recieved: %s ", marshaled)
	}

	unmarshaled, err = unmarshal(marshaled)

	if err != nil {
		t.Errorf("unmarshal returned an err: %s", err.Error())
	}

	if len(unmarshaled) != 0 {
		t.Errorf("Expected unmarshal(marshal(nil)) to return an empty map but recieved: %s", unmarshaled)
	}

	m = make(map[string]string)

	marshaled, err = marshal(m)

	if err != nil {
		t.Errorf("marshal() returned an err: %s", err.Error())
	}

	if marshaled != "{}" {
		t.Errorf("Expected marshal() with empty map to return {} but got %s", marshaled)
	}

	unmarshaled, err = unmarshal(marshaled)

	if err != nil {
		t.Errorf("unmarshal returned an err: %s", err.Error())
	}

	if len(unmarshaled) != 0 {
		t.Errorf("Expected unmarshal(marshal([])) to return an empty map but recieved: %s", unmarshaled)
	}
}

func TestRemediation(t *testing.T) {
	machine, machineNamespacedName := getMachine("machine1")
	host, hostNamespacedName := getBareMetalHost("host1")
	node, nodeNamespacedName := getNode("node1")
	nodeAnnotations := map[string]string{"annName1": "annValue1", "annName2": "annValue2"}
	nodeLabels := map[string]string{"labelName1": "labelValue1", "labelName2": "labelValue2"}

	node.Annotations = nodeAnnotations
	node.Labels = nodeLabels
	linkMachineAndNode(machine, node)
	host.Status.PoweredOn = true

	//starting test with machine that needs remediation
	machine.Annotations = make(map[string]string)
	machine.Annotations[HostAnnotation] = host.Namespace + "/" + host.Name
	machine.Annotations[externalRemediationAnnotation] = ""

	scheme := runtime.NewScheme()
	machinev1beta1.AddToScheme(scheme)
	bmoapis.AddToScheme(scheme)
	corev1.AddToScheme(scheme)

	c := fakeclient.NewFakeClientWithScheme(scheme)

	actuator, err := NewActuator(ActuatorParams{
		Client: c,
	})
	if err != nil {
		t.Error(err)
	}

	c.Create(context.TODO(), machine)
	c.Create(context.TODO(), host)
	c.Create(context.TODO(), node)

	err = actuator.Update(context.TODO(), machine)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	host = &bmh.BareMetalHost{}
	c.Get(context.TODO(), hostNamespacedName, host)
	if !hasPowerOffRequestAnnotation(host) {
		t.Log("Expected reboot annotation on the host but none found")
		t.Fail()
	}

	host.Status.PoweredOn = false
	c.Update(context.TODO(), host)

	err = actuator.Update(context.TODO(), machine)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	machine = &machinev1beta1.Machine{}
	c.Get(context.TODO(), machineNamespacedName, machine)

	if _, exists := machine.Annotations[poweredOffForRemediation]; !exists {
		t.Log("Expected powered-off-for-remediation annotation to exist on machine but none found")
		t.Fail()
	}

	node = &corev1.Node{}
	err = c.Get(context.TODO(), nodeNamespacedName, node)
	nodeBackup := node.DeepCopy()

	machine = &machinev1beta1.Machine{}
	c.Get(context.TODO(), machineNamespacedName, machine)
	err = actuator.Update(context.TODO(), machine)
	if err == nil {
		t.Errorf("expected a requeue err but err was nil")
	} else {
		switch err.(type) {
		case *machineapierrors.RequeueAfterError:
			break
		default:
			t.Errorf("unexpected error %v", err)
		}
	}

	node = &corev1.Node{}
	err = c.Get(context.TODO(), nodeNamespacedName, node)
	if !errors.IsNotFound(err) {
		t.Log("Expected node to be deleted")
		t.Fail()
	}

	//fake client doesn't respect finalizers and deletes the node object
	//we re-create it with deletion timestamp as this what we expect
	//from k8s to do
	now := metav1.Now()
	nodeBackup.SetDeletionTimestamp(&now)

	nodeBackup.ResourceVersion = "" //create won't work with non empty ResourceVersion
	c.Create(context.TODO(), nodeBackup)

	err = actuator.Update(context.TODO(), machine)
	expectRequetAfterError(err, t)
	node = &corev1.Node{}
	c.Get(context.TODO(), nodeNamespacedName, node)

	if len(node.Finalizers) > 0 {
		t.Log("Expected finalizer to be removed but finalizer still exist on node")
		t.Fail()
	}

	//fake client won't delete the node even though it has no finalizers anymore, so we delete it
	err = c.Delete(context.TODO(), nodeBackup)
	err = c.Delete(context.TODO(), node)

	err = actuator.Update(context.TODO(), machine)
	host = &bmh.BareMetalHost{}
	c.Get(context.TODO(), hostNamespacedName, host)

	if hasPowerOffRequestAnnotation(host) {
		t.Log("Expected reboot annotation to be removed but it's still there")
		t.Fail()
	}

	//simulate power on and node that comes up again
	host.Status.PoweredOn = true
	c.Update(context.TODO(), host)

	node, _ = getNode("node1")
	linkMachineAndNode(machine, node)
	c.Create(context.TODO(), node)
	c.Update(context.TODO(), machine)

	machine = &machinev1beta1.Machine{}
	c.Get(context.TODO(), machineNamespacedName, machine)

	err = actuator.Update(context.TODO(), machine)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	node = &corev1.Node{}
	c.Get(context.TODO(), nodeNamespacedName, node)

	assert.Equal(t, nodeAnnotations, node.Annotations,
		"Node annotations before remediation and after remediation ares not equal")

	assert.Equal(t, nodeLabels, node.Labels,
		"Node labels before remediation and after remediation ares not equal")

	machine = &machinev1beta1.Machine{}
	c.Get(context.TODO(), machineNamespacedName, machine)
	err = actuator.Update(context.TODO(), machine)

	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	machine = &machinev1beta1.Machine{}
	c.Get(context.TODO(), machineNamespacedName, machine)

	if len(machine.Annotations) > 0 {
		if _, exists := machine.Annotations[externalRemediationAnnotation]; exists {
			t.Log("Expected external remediation annotation to be removed but it's still there")
			t.Fail()
		}

		if _, exists := machine.Annotations[poweredOffForRemediation]; exists {
			t.Log("Expected powered-off-for-remediation annotation to be removed but it's still there")
			t.Fail()
		}
	}

	//make sure nothing happens after remediation
	err = actuator.Update(context.TODO(), machine)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}
	err = actuator.Update(context.TODO(), machine)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	host = &bmh.BareMetalHost{}
	c.Get(context.TODO(), hostNamespacedName, host)

	if hasPowerOffRequestAnnotation(host) {
		t.Log("Expected reboot annotation to be removed but it's still there, maybe reboot loops?")
		t.Fail()
	}
}

func expectRequetAfterError(err error, t *testing.T) {
	if err == nil {
		t.Errorf("expected a requeue err but err was nil")
	} else {
		switch err.(type) {
		case *machineapierrors.RequeueAfterError:
			break
		default:
			t.Errorf("unexpected error %v", err)
		}
	}
}

func getNode(name string) (*corev1.Node, types.NamespacedName) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: testRemediationNamespace,
	},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Node",
			APIVersion: corev1.SchemeGroupVersion.String(),
		}}

	nodeNamespacedName := types.NamespacedName{
		Namespace: node.ObjectMeta.Namespace,
		Name:      node.ObjectMeta.Name,
	}

	return node, nodeNamespacedName
}

func getBareMetalHost(name string) (*bmh.BareMetalHost, types.NamespacedName) {
	host := &bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "BareMetalHost",
			APIVersion: bmh.SchemeGroupVersion.String(),
		},
		Spec:   bmh.BareMetalHostSpec{},
		Status: bmh.BareMetalHostStatus{},
	}

	hostNamespacedName := types.NamespacedName{
		Namespace: host.ObjectMeta.Namespace,
		Name:      host.ObjectMeta.Name,
	}

	return host, hostNamespacedName
}

func getMachine(name string) (*machinev1beta1.Machine, types.NamespacedName) {
	machine := &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testRemediationNamespace,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Machine",
			APIVersion: machinev1beta1.SchemeGroupVersion.String(),
		},
		Status: machinev1beta1.MachineStatus{},
	}

	machineNamespacedName := types.NamespacedName{
		Namespace: machine.ObjectMeta.Namespace,
		Name:      machine.ObjectMeta.Name,
	}

	return machine, machineNamespacedName
}

func linkMachineAndNode(machine *machinev1beta1.Machine, node *corev1.Node) {
	machine.Status.NodeRef = &corev1.ObjectReference{
		Kind:      "Node",
		Namespace: testRemediationNamespace,
		Name:      node.Name,
	}
}
