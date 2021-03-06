package main

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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
	Debug    bool
	Create   bool
	Revoke   bool
	Rotate   bool
	Team     string
}

func DefaultConfig() *Config {
	return &Config{
		Clusters: []string{"dev-fss", "dev-sbs", "prod-fss", "prod-sbs"},
	}
}

func (c *Config) addFlags() {
	flag.StringSliceVar(&c.Clusters, "clusters", c.Clusters, "Which clusters to operate on.")
	flag.StringVar(&c.Team, "team", c.Team, "Team name that will own the configuration file.")
	flag.BoolVar(&c.Debug, "debug", c.Debug, "Print debugging information.")
	flag.BoolVar(&c.Create, "create", c.Create, "Create teams that do not exist.")
	flag.BoolVar(&c.Revoke, "revoke", c.Revoke, "Delete any tokens that belongs to this team.")
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

func ServiceAccountName(team string) string {
	return fmt.Sprintf(ServiceUserTemplate, team)
}

func ServiceAccount(client kubernetes.Interface, serviceAccountName string) (*v1.ServiceAccount, error) {
	log.Debugf("attempting to retrieve service account '%s' in namespace %s", serviceAccountName, Namespace)
	return client.CoreV1().ServiceAccounts(Namespace).Get(serviceAccountName, metav1.GetOptions{})
}

func DeleteServiceAccount(client kubernetes.Interface, serviceAccountName string) error {
	log.Debugf("attempting to delete service account '%s' in namespace %s", serviceAccountName, Namespace)
	return client.CoreV1().ServiceAccounts(Namespace).Delete(serviceAccountName, &metav1.DeleteOptions{})
}

func CreateServiceAccount(client kubernetes.Interface, serviceAccountName string) (*v1.ServiceAccount, error) {
	log.Debugf("attempting to create service account '%s' in namespace %s", serviceAccountName, Namespace)
	serviceAccount := v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: Namespace,
		},
	}
	return client.CoreV1().ServiceAccounts(Namespace).Create(&serviceAccount)
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
		Token: string(secret.Data["token"]),
	}
}

func clusterExec(cluster string, userConfig *clientcmdapi.Config) error {
	clientConfig, err := buildConfigFromFlags(cluster, os.Getenv("KUBECONFIG"))
	if err != nil {
		return err
	}

	client, err := KubeClient(clientConfig)
	if err != nil {
		return err
	}

	serviceAccountName := ServiceAccountName(config.Team)
	deleted := false

	// if revoking access or rotating keys, delete the service account if it exists
	if config.Rotate || config.Revoke {
		err = DeleteServiceAccount(client, serviceAccountName)
		if err == nil {
			if config.Revoke {
				log.Infof("%s: revoked access for service account '%s'", cluster, serviceAccountName)
				return nil
			}
			deleted = true
		} else {
			if errors.IsNotFound(err) && !config.Create {
				log.Debugf("%s: service account '%s' not found", cluster, serviceAccountName)
			} else {
				return fmt.Errorf("while deleting service account: %s", err)
			}
		}
	}

	// create service account
	if config.Rotate || config.Create {
		_, err = CreateServiceAccount(client, serviceAccountName)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				log.Debugf("%s: service account '%s' already exists", cluster, serviceAccountName)
			} else {
				return fmt.Errorf("while creating service account: %s", err)
			}
		} else if config.Rotate && deleted {
			log.Infof("%s: rotated token for service account '%s'", cluster, serviceAccountName)
		} else if config.Create {
			log.Infof("%s: created service account '%s'", cluster, serviceAccountName)
		}

		// Sleep for a bit to allow server to generate token
		time.Sleep(100 * time.Millisecond)
	}

	// get service account for this team
	serviceAccount, err := ServiceAccount(client, serviceAccountName)
	if err != nil {
		return fmt.Errorf("while retrieving service account: %s", err)
	}

	// get service account secret token
	secret, err := ServiceAccountSecret(client, *serviceAccount)
	if err != nil {
		return fmt.Errorf("while retrieving secret token: %s", err)
	}

	authInfo := AuthInfo(*secret)

	userConfig.AuthInfos[cluster] = &authInfo
	userConfig.Clusters[cluster] = &clientcmdapi.Cluster{
		Server:                clientConfig.Host,
	}
	userConfig.Contexts[cluster] = &clientcmdapi.Context{
		Namespace: "default",
		AuthInfo:  cluster,
		Cluster:   cluster,
	}

	return nil
}

func run() error {
	config.addFlags()
	flag.Parse()

	if config.Debug {
		log.SetLevel(log.TraceLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	log.SetOutput(os.Stderr)

	if len(config.Team) == 0 {
		flag.Usage()
		return fmt.Errorf("team name must be specified")
	}

	if config.Revoke && (config.Create || config.Rotate) {
		return fmt.Errorf("--revoke is mutually exclusive with --create and --rotate")
	}

	failed := false
	userConfig := clientcmdapi.NewConfig()

	for _, cluster := range config.Clusters {
		log.Debugf("%s: entering cluster", cluster)

		err := clusterExec(cluster, userConfig)

		if err == nil {
			log.Debugf("%s: successfully generated configuration", cluster)
		} else {
			log.Errorf("%s: %s", cluster, err)
			failed = true
		}
	}

	if failed {
		return fmt.Errorf("exiting due to errors")
	}

	if config.Revoke {
		log.Infof("successfully revoked keys")
		return nil
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
	log.Debugf("configuration file written to stdout")

	return nil
}

func main() {
	err := run()
	if err != nil {
		log.Errorf("fatal: %s", err)
		os.Exit(1)
	}
}
