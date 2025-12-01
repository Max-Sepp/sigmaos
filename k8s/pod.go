package k8s

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	db "sigmaos/debug"
	"sigmaos/proc"
)

const (
	BIN_DIR   = "/tmp/sigmaos-realm-bins-k8s"
	NAMESPACE = "default"
)

type K8sPod struct {
	mu        sync.Mutex
	cond      *sync.Cond
	p         *proc.Proc
	clientset *kubernetes.Clientset
	podName   string

	sandboxExited bool
	exitErr       error
}

func StartK8sPod(p *proc.Proc, clientset *kubernetes.Clientset) (*K8sPod, error) {
	imgName := "sigmauser"
	// Set some environment variables
	p.AppendEnv("PATH", "/bin:/bin2:/usr/bin:/home/sigmaos/bin/kernel")
	p.AppendEnv("SIGMA_EXEC_TIME", strconv.FormatInt(time.Now().UnixMicro(), 10))
	b, err := time.Now().MarshalText()
	if err != nil {
		db.DFatalf("Error marshal timestamp pb: %v", err)
	}
	p.AppendEnv("SIGMA_EXEC_TIME_PB", string(b))
	p.AppendEnv("SIGMA_SPAWN_TIME", strconv.FormatInt(p.GetSpawnTime().UnixMicro(), 10))
	p.AppendEnv(proc.SIGMAPERF, p.GetProcEnv().GetPerf())

	// Create pod name from process ID
	podName := "k8s-pod-" + p.GetPid().String()

	// Build environment variables slice
	var envVars []corev1.EnvVar
	for key, val := range p.Env {
		envVars = append(envVars, corev1.EnvVar{
			Name:  key,
			Value: val,
		})
	}

	pn := filepath.Join(BIN_DIR, p.GetVersionedProgram())

	// Create the pod specification
	podSpec := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: NAMESPACE,
			Labels: map[string]string{
				"sigmaos-pid": p.GetPid().String(),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "sigmauser-ctr",
					Image:   imgName, // Assuming program is a container image
					Command: append([]string{pn}, p.GetArgs()...),
					Env:     envVars,
				},
			},
		},
	}

	// Create the pod using the Kubernetes API
	ctx := context.Background()
	createdPod, err := clientset.CoreV1().Pods(NAMESPACE).Create(ctx, podSpec, metav1.CreateOptions{})
	if err != nil {
		db.DPrintf(db.ERROR, "Failed to create pod: %v", err)
		return nil, fmt.Errorf("failed to create pod: %v", err)
	}

	db.DPrintf(db.K8S, "Created pod %s in NAMESPACE %s", createdPod.Name, NAMESPACE)

	pod := &K8sPod{
		p:         p,
		clientset: clientset,
		podName:   podName,
	}
	pod.cond = sync.NewCond(&pod.mu)

	// Start watching the pod status
	go pod.watchPodStatus()

	return pod, nil
}

func (pod *K8sPod) Pid() int {
	// For K8s pods, we don't have a traditional PID
	// Return a synthetic value or 0
	return 0
}

func (pod *K8sPod) String() string {
	return fmt.Sprintf("&{pid: %v, podName: %v, NAMESPACE: %v}", pod.p.GetPid(), pod.podName, NAMESPACE)
}

// watchPodStatus monitors the pod status and updates internal state
func (pod *K8sPod) watchPodStatus() {
	ctx := context.Background()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p, err := pod.clientset.CoreV1().Pods(NAMESPACE).Get(ctx, pod.podName, metav1.GetOptions{})
			if err != nil {
				db.DPrintf(db.ERROR, "Failed to get pod status: %v", err)
				pod.mu.Lock()
				pod.sandboxExited = true
				pod.exitErr = err
				pod.cond.Broadcast()
				pod.mu.Unlock()
				return
			}

			// Check if pod has terminated
			if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				pod.mu.Lock()
				pod.sandboxExited = true
				if p.Status.Phase == corev1.PodFailed {
					pod.exitErr = fmt.Errorf("pod failed with phase: %v", p.Status.Phase)
				}
				pod.cond.Broadcast()
				pod.mu.Unlock()
				db.DPrintf(db.K8S, "Pod %s terminated with phase: %v", pod.podName, p.Status.Phase)
				return
			}
		}
	}
}

func (pod *K8sPod) Wait() error {
	pod.mu.Lock()
	defer pod.mu.Unlock()

	// Wait for the pod to exit
	for !pod.sandboxExited {
		pod.cond.Wait()
	}

	return pod.exitErr
}

func (pod *K8sPod) Kill() error {
	ctx := context.Background()

	// Delete the pod
	deletePolicy := metav1.DeletePropagationForeground
	err := pod.clientset.CoreV1().Pods(NAMESPACE).Delete(ctx, pod.podName, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	})
	if err != nil {
		db.DPrintf(db.ERROR, "Failed to delete pod %s: %v", pod.podName, err)
		return fmt.Errorf("failed to delete pod: %v", err)
	}

	db.DPrintf(db.K8S, "Deleted pod %s", pod.podName)

	pod.mu.Lock()
	pod.sandboxExited = true
	pod.cond.Broadcast()
	pod.mu.Unlock()

	return nil
}
