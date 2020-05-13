package wrapper

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type nodeMapper struct{}

const MachineAnnotation = "machine.openshift.io/machine"

// Map will return a reconcile request for a Machine if the event is for a
// BareMetalHost and that BareMetalHost references a Machine.
func (m *nodeMapper) Map(obj handler.MapObject) []reconcile.Request {
	if node, ok := obj.Object.(*corev1.Node); ok {
		machineKey, ok := node.Annotations[MachineAnnotation]
		if !ok {
			return []reconcile.Request{}
		}

		namespace, machineName, err := cache.SplitMetaNamespaceKey(machineKey)
		if err != nil {
			return []reconcile.Request{}
		}

		return []reconcile.Request{
			reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      machineName,
				},
			},
		}
	}
	return []reconcile.Request{}
}
