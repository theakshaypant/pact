package cluster

import (
	"fmt"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// clientsFactory builds the cluster clients pact needs from kubeconfig
// content; injectable so tests can fake the cluster entirely.
type clientsFactory func(kubeconfig []byte) (objectClient, deploymentLister, error)

// newKubeClients builds a dynamic object client (with a discovery-built
// RESTMapper) and a typed deployment lister from kubeconfig content.
func newKubeClients(kubeconfig []byte) (objectClient, deploymentLister, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building dynamic client: %w", err)
	}

	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building discovery client: %w", err)
	}
	groupResources, err := restmapper.GetAPIGroupResources(disc)
	if err != nil {
		return nil, nil, fmt.Errorf("discovering API groups: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("building kubernetes clientset: %w", err)
	}

	return &dynamicObjectClient{
		client: dyn,
		mapper: restmapper.NewDiscoveryRESTMapper(groupResources),
	}, &deploymentClient{client: clientset}, nil
}
