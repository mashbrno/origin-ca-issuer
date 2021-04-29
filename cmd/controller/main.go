package main

import (
	"net/http"
	"os"
	"time"

	"github.com/butonic/zerologr"
	"github.com/cloudflare/origin-ca-issuer/cmd/controller/options"
	"github.com/cloudflare/origin-ca-issuer/internal/cfapi"
	v1 "github.com/cloudflare/origin-ca-issuer/pkgs/apis/v1"
	"github.com/cloudflare/origin-ca-issuer/pkgs/controllers"
	"github.com/cloudflare/origin-ca-issuer/pkgs/provisioners"
	certmanager "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

func main() {
	fs := pflag.CommandLine
	o := options.NewControllerOptions()
	o.AddFlags(fs)
	kubeconfig := buildKubeConfig(fs)

	_ = fs.Parse(os.Args[1:])

	logf.SetLogger(zerologr.New())
	log := logf.Log.WithName("origin-issuer").V(8)

	if err := o.Validate(); err != nil {
		log.Error(err, "error validating options")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "could not add to scheme")
		os.Exit(1)
	}
	if err := certmanager.AddToScheme(scheme); err != nil {
		log.Error(err, "could not add to scheme")
		os.Exit(1)
	}
	if err := v1.Install(scheme); err != nil {
		log.Error(err, "could not add to scheme")
		os.Exit(1)
	}

	kubeCfg, err := kubeconfig.ClientConfig()
	if err != nil {
		log.Error(err, "could not load kubeconfig")
		os.Exit(1)
	}

	kubeCfg.QPS = o.KubernetesAPIQPS
	kubeCfg.Burst = o.KubernetesAPIBurst

	mgr, err := manager.New(kubeCfg, manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Error(err, "could not create manager")
		os.Exit(1)
	}

	collection := provisioners.CollectionWith(nil)

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	f := cfapi.FactoryFunc(func(serviceKey []byte) (cfapi.Interface, error) {
		return cfapi.New(serviceKey, cfapi.WithClient(httpClient)), nil
	})

	err = builder.
		ControllerManagedBy(mgr).
		For(&v1.OriginIssuer{}).
		Complete(&controllers.OriginIssuerController{
			Client:     mgr.GetClient(),
			Clock:      clock.RealClock{},
			Factory:    f,
			Log:        log.WithName("controllers").WithName("OriginIssuer"),
			Collection: collection,
		})

	if err != nil {
		log.Error(err, "could not create origin issuer controller")
		os.Exit(1)
	}

	err = builder.
		ControllerManagedBy(mgr).
		For(&certmanager.CertificateRequest{}).
		Complete(&controllers.CertificateRequestController{
			Client:     mgr.GetClient(),
			Log:        log.WithName("controllers").WithName("CertificateRequest"),
			Collection: collection,
		})

	if err != nil {
		log.Error(err, "could not create certificaterequest controller")
		os.Exit(1)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "could not start manager")
		os.Exit(1)
	}
}

func buildKubeConfig(fs *pflag.FlagSet) clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	clientcmd.BindOverrideFlags(configOverrides, fs, clientcmd.RecommendedConfigOverrideFlags(""))
	fs.StringVar(&loadingRules.ExplicitPath, clientcmd.RecommendedConfigPathFlag, loadingRules.ExplicitPath, "Path to the kubeconfig file to use. If not set, attempts autoconfiguration")

	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
}
