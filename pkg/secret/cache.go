package secret

import (
	"context"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/credentialprovider"
	execplugin "k8s.io/kubernetes/pkg/credentialprovider/plugin"
	credential "k8s.io/kubernetes/pkg/credentialprovider/secrets"
	"os"
	"time"

	// register credential providers
	_ "k8s.io/kubernetes/pkg/credentialprovider/aws"
	_ "k8s.io/kubernetes/pkg/credentialprovider/azure"
	_ "k8s.io/kubernetes/pkg/credentialprovider/gcp"
)

type Cache interface {
	GetDockerKeyring(ctx context.Context, secret, secretNS, pod, podNS string) (credentialprovider.DockerKeyring, error)
}

func CreateCacheOrDie(pluginConfigFile, pluginBinDir string) Cache {
	if len(pluginConfigFile) > 0 && len(pluginBinDir) > 0 {
		if err := execplugin.RegisterCredentialProviderPlugins(pluginConfigFile, pluginBinDir); err != nil {
			klog.Fatalf("unable to register the credential plugin through %q and %q: %s", pluginConfigFile,
				pluginBinDir, err)
		}
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("unable to get cluster config: %s", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("unable to get cluster client: %s", err)
	}

	c := secretWOCache{
		k8sCliSet: clientset,
		keyring:   credentialprovider.NewDockerKeyring(),
	}

	curNamespace, err := ioutil.ReadFile("/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		klog.Warningf("unable to fetch the current namespace from the sa volume: %q", err.Error())
		return c
	}

	curPod := os.Getenv("POD_NAME")
	if len(curPod) == 0 {
		klog.Warning(`unable to fetch the current pod name from env "POD_NAME"`)
		return c
	}

	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()
	namespace := string(curNamespace)
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, curPod, metav1.GetOptions{})
	if err != nil {
		klog.Fatalf(`unable to fetch the daemon pod "%s/%s": %s`, namespace, curPod, err)
	}

	c.daemonSecrets = make([]secretPair, len(pod.Spec.ImagePullSecrets))
	klog.Infof(
		`got %d imagePullSecrets from the daemon pod %s/%s`, len(pod.Spec.ImagePullSecrets), namespace, curPod,
	)
	for i := range pod.Spec.ImagePullSecrets {
		c.daemonSecrets[i] = secretPair{pod.Spec.ImagePullSecrets[i].Name, namespace}
	}

	return c
}

type secretPair struct {
	name      string
	namespace string
}

// FIXME we need to somehow cache and watch remote secrets to reuse them and prevent always retrieving same secrets.
type secretWOCache struct {
	k8sCliSet     *kubernetes.Clientset
	daemonSecrets []secretPair
	keyring       credentialprovider.DockerKeyring
}

func (s secretWOCache) GetDockerKeyring(
	ctx context.Context, secret, secretNS, pod, podNS string,
) (keyring credentialprovider.DockerKeyring, err error) {
	var secrets []corev1.Secret
	if len(secret) > 0 {
		secret, err := s.k8sCliSet.CoreV1().Secrets(secretNS).Get(ctx, secret, metav1.GetOptions{})
		if err != nil {
			klog.Errorf(`unable to fetch secret "%s/%s": %s`, secretNS, secret, err)
			return nil, err
		}

		secrets = append(secrets, *secret)
	}

	if len(pod) > 0 {
		pod, err := s.k8sCliSet.CoreV1().Pods(podNS).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			klog.Errorf(`unable to fetch pod "%s/%s": %s`, podNS, pod, err)
			return nil, err
		}

		klog.Infof(
			`got %d imagePullSecrets from the workload pod %s/%s`, len(pod.Spec.ImagePullSecrets),
			pod.Namespace, pod.Name,
		)

		for _, secretRef := range pod.Spec.ImagePullSecrets {
			secret, err := s.k8sCliSet.CoreV1().Secrets(podNS).Get(ctx, secretRef.Name, metav1.GetOptions{})
			if err != nil {
				klog.Errorf(`unable to fetch secret "%s/%s": %s`, podNS, secretRef.Name, err)
				return nil, err
			}

			secrets = append(secrets, *secret)
		}
	}

	for _, pair := range s.daemonSecrets {
		secret, err := s.k8sCliSet.CoreV1().Secrets(pair.namespace).Get(ctx, pair.name, metav1.GetOptions{})
		if err != nil {
			klog.Errorf(`unable to fetch secret "%s/%s": %s`, pair.namespace, pair.name, err)
			return nil, err
		}

		secrets = append(secrets, *secret)
	}

	keyRing, err := credential.MakeDockerKeyring(secrets, s.keyring)
	if err != nil {
		klog.Errorf("unable to create keyring: %s", err)
	}

	return keyRing, err
}
