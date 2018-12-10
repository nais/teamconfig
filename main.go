package main

import (
	"bufio"
	"fmt"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth" // Needed for azure auth side effect

	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const Namespace = "default"
const ServiceUserTemplate = "serviceuser-%s"

type Config struct {
	Clusters []string
	Team     string
	Create   bool
	Rotate   bool
}

func DefaultConfig() *Config {
	return &Config{
		Clusters: []string{"preprod-fss", "preprod-sbs", "prod-fss", "prod-sbs"},
	}
}

func (c *Config) addFlags() {
	flag.StringSliceVar(&c.Clusters, "clusters", c.Clusters, "Which clusters to operate on.")
	flag.StringVar(&c.Team, "team", c.Team, "Team name that will own the configuration file.")
	flag.BoolVar(&c.Create, "create", c.Create, "Create teams that do not exist.")
	flag.BoolVar(&c.Rotate, "rotate", c.Rotate, "Rotate secret tokens that are already present in cluster. This will invalidate old tokens.")
}

var config = DefaultConfig()

func buildConfigFromFlags(context, kubeconfigPath string) (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()
}

func KubeClient(config *rest.Config) (kubernetes.Interface, error) {
	return kubernetes.NewForConfig(config)
}

func ServiceAccount(client kubernetes.Interface, team string) (*v1.ServiceAccount, error) {
	serviceAccountName := fmt.Sprintf(ServiceUserTemplate, team)
	log.Debugf("attempting to retrieve service account '%s' in namespace %s", serviceAccountName, Namespace)
	return client.CoreV1().ServiceAccounts(Namespace).Get(serviceAccountName, metav1.GetOptions{})
}

func ServiceAccountSecret(client kubernetes.Interface, serviceAccount v1.ServiceAccount) (*v1.Secret, error) {
	if len(serviceAccount.Secrets) == 0 {
		return nil, fmt.Errorf("no secret associated with service account '%s'", serviceAccount.Name)
	}
	secretRef := serviceAccount.Secrets[0]
	log.Debugf("attempting to retrieve secret '%s' in namespace %s", secretRef.Name, Namespace)
	return client.CoreV1().Secrets(Namespace).Get(secretRef.Name, metav1.GetOptions{})
}

func AuthInfo(secret v1.Secret) clientcmdapi.AuthInfo {
	return clientcmdapi.AuthInfo{
		Token:    string(secret.Data["token"]),
	}
}

func TeamAuthInfo(client kubernetes.Interface, team string) (*clientcmdapi.AuthInfo, error) {
	serviceAccount, err := ServiceAccount(client, team)
	if err != nil {
		return nil, fmt.Errorf("while retrieving service account: %s", err)
	}

	secret, err := ServiceAccountSecret(client, *serviceAccount)
	if err != nil {
		return nil, fmt.Errorf("while retrieving secret token: %s", err)
	}

	authInfo := AuthInfo(*secret)
	return &authInfo, nil
}

func run() error {
	config.addFlags()
	flag.Parse()
	log.SetLevel(log.TraceLevel)
	log.SetOutput(os.Stderr)

	if len(config.Team) == 0 {
		return fmt.Errorf("team name must be specified")
	}

	userConfig := clientcmdapi.NewConfig()

	for _, cluster := range config.Clusters {
		log.Debugf("entering cluster '%s'", cluster)

		clientConfig, err := buildConfigFromFlags(cluster, os.Getenv("KUBECONFIG"))
		if err != nil {
			return err
		}

		client, err := KubeClient(clientConfig)
		if err != nil {
			return err
		}

		authInfo, err := TeamAuthInfo(client, config.Team)
		if err != nil {
			return err
		}

		userConfig.AuthInfos[cluster] = authInfo
		userConfig.Clusters[cluster] = &clientcmdapi.Cluster{
			InsecureSkipTLSVerify: true,
			Server:                clientConfig.Host,
		}
		userConfig.Contexts[cluster] = &clientcmdapi.Context{
			Namespace: "default",
			AuthInfo:  cluster,
			Cluster:   cluster,
		}
	}

	userConfig.CurrentContext = config.Clusters[0]

	output, err := clientcmd.Write(*userConfig)
	if err != nil {
		return fmt.Errorf("while generating output: %s", err)
	}

	stdout := bufio.NewWriter(os.Stdout)
	_, err = stdout.Write(output)
	stdout.Flush()

	if err != nil {
		return fmt.Errorf("while writing output: %s", err)
	}
	log.Debugf("configuration file successfully written to stdout")

	return nil
}

func main() {
	err := run()
	if err != nil {
		log.Errorf("Fatal error: %s", err)
		os.Exit(1)
	}
}
