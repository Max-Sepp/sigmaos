package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	db "sigmaos/debug"
)

// NewClnt creates a new Kubernetes clientset from kubeconfig content
func NewClnt(kubeconfigContent string) (*kubernetes.Clientset, error) {
	// Build config from kubeconfig content
	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigContent))
	if err != nil {
		db.DPrintf(db.ERROR, "Failed to build config from kubeconfig: %v", err)
		return nil, fmt.Errorf("failed to build config from kubeconfig: %v", err)
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		db.DPrintf(db.ERROR, "Failed to create Kubernetes clientset: %v", err)
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %v", err)
	}

	db.DPrintf(db.K8S, "Successfully created Kubernetes clientset")
	return clientset, nil
}
