package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/secretflow/kuscia/cmd/kuscia/confloader"
	"github.com/secretflow/kuscia/pkg/common"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	yaml2 "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kusciacrd "github.com/secretflow/kuscia/pkg/crd/apis/kuscia/v1alpha1"
	kusciaapi "github.com/secretflow/kuscia/pkg/crd/clientset/versioned/typed/kuscia/v1alpha1"
)

const (
	homePath       = "/home/kuscia"
	kusciaPath     = "/home/kuscia/bin/kuscia"
	confPath       = "/home/kuscia/var/kuscia.yaml"
	stdoutPath     = "/home/kuscia/var/logs/stdout.log"
	stderrPath     = "/home/kuscia/var/logs/stderr.log"
	k3sCertPath    = "/home/kuscia/var/k3s/server/tls/server-ca.crt"
	kubeconfigPath = "/home/kuscia/etc/kubeconfig"
	domainCertPath = "/home/kuscia/var/certs/domain.crt"
	containerdPath = "/home/kuscia/containerd/run/containerd.sock"
	publicDataPath = "/home/kuscia/mnt/public"
	sharedDataPath = "/home/kuscia/mnt/shared"

	k3sHTTP   = "https://127.0.0.1:6443"
	envoyPort = 1080
)

type ClusterOptions struct {
	SelfDomain string
	PeerDomain string
	Images     []string
}

func main() {
	zap.ReplaceGlobals(zap.Must(zap.NewDevelopment()))

	command := &cobra.Command{
		Use: "bootstrap",
		Run: func(cmd *cobra.Command, args []string) {
			options := ClusterOptions{}
			options.SelfDomain = cmd.Flag("self").Value.String()
			options.PeerDomain = cmd.Flag("peer").Value.String()
			options.Images, _ = cmd.Flags().GetStringArray("load-image")
			run(options)
		},
	}

	command.PersistentFlags().String("self", "", "domain ID for this node")
	command.MarkFlagRequired("self")

	command.PersistentFlags().String("peer", "", "domain ID for the peer node")
	command.MarkFlagRequired("peer")

	command.PersistentFlags().StringArray("load-image", []string{}, "load image from this tarfile into the container's image registry")

	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (o ClusterOptions) PeerDomainAsAnnotation() string {
	return fmt.Sprintf("domain/%s", o.PeerDomain)
}

func (o ClusterOptions) DomainRouteAsName() string {
	return fmt.Sprintf("domain-route-%s-to-%s", o.SelfDomain, o.PeerDomain)
}

func newKusciaConfig(options ClusterOptions) []byte {
	if errs := validation.IsDNS1035Label(options.SelfDomain); len(errs) > 0 {
		zap.L().Fatal("invalid domain ID", zap.Strings("errors", errs))
	}

	keyData := newEncodedRSAKey()

	config := confloader.AutomonyKusciaConfig{
		Runtime: "runc",
		CommonConfig: confloader.CommonConfig{
			Mode:          common.RunModeAutonomy,
			LogLevel:      "DEBUG",
			DomainID:      options.SelfDomain,
			DomainKeyData: keyData,
			Protocol:      common.NOTLS,
		},
	}

	configText, err := yaml.Marshal(config)

	if err != nil {
		zap.L().Fatal("failed to marshal config", zap.Error(err))
	}

	return configText
}

func ensureParent(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		zap.L().Fatal("failed to create directory", zap.Error(err))
	}
}

func tryWithTimeout(name string, fn func() (bool, error), timeout int, interval int) error {
	c := make(chan error, 1)

	go func() {
		for {
			zap.S().Info("checking ", name)
			stop, err := fn()
			if stop {
				c <- err
				break
			} else {
				if err != nil {
					zap.S().Debugf("checking %s: %s", name, err)
				}
				time.Sleep(time.Duration(interval) * time.Second)
			}
		}
	}()

	select {
	case err := <-c:
		return err
	case <-time.After(time.Duration(timeout) * time.Second):
		return fmt.Errorf("timed out checking %s", name)
	}
}

func probeHTTPEndpoint(url string, tlsCertPath string) func() (bool, error) {
	client := new(http.Client)

	maybeWithCertificate := func() {
		cert, _ := os.ReadFile(tlsCertPath)
		if cert == nil {
			zap.S().Debug("no certificate found at ", tlsCertPath)
			return
		}
		rootCAs, _ := x509.SystemCertPool()
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		if ok := rootCAs.AppendCertsFromPEM(cert); !ok {
			zap.S().Warn("failed to append certificate at ", tlsCertPath)
			return
		}
		tlsConfig := &tls.Config{
			InsecureSkipVerify: false,
			RootCAs:            rootCAs,
		}
		transport := &http.Transport{
			TLSClientConfig: tlsConfig,
		}
		zap.S().Debug("using certificate at ", tlsCertPath)
		client.Transport = transport
	}

	probe := func() (bool, error) {
		maybeWithCertificate()
		resp, err := client.Get(url)
		if err != nil {
			return false, err
		}
		switch resp.StatusCode {
		case 200:
			return true, nil
		case 401:
			return true, nil
		default:
			defer resp.Body.Close()
			var msg error
			if body, err := io.ReadAll(resp.Body); err == nil {
				msg = fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, body)
			} else {
				msg = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			}
			return true, msg
		}
	}

	return probe
}

func waitForFile(out chan struct {
	content []byte
	err     error
}, path string, timeout int) {
	if content, err := os.ReadFile(path); err == nil {
		out <- struct {
			content []byte
			err     error
		}{
			content: content,
			err:     nil,
		}
		return
	}

	watcher, err := fsnotify.NewWatcher()

	if err != nil {
		out <- struct {
			content []byte
			err     error
		}{
			content: nil,
			err:     err,
		}
		return
	}

	defer watcher.Close()

	doneWatching := make(chan struct{})

	watcher.Add(filepath.Dir(path))

	go func() {
		defer close(doneWatching)
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					continue
				}
				if (event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) && event.Name == path {
					waitForFile(out, path, timeout)
					return
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					continue
				}
				zap.S().Debug("watcher error: ", err)
			}
		}
	}()

	select {
	case <-doneWatching:
		return
	case <-time.After(time.Duration(timeout) * time.Second):
		out <- struct {
			content []byte
			err     error
		}{
			content: nil,
			err:     fmt.Errorf("timed out waiting for file %s", path),
		}
		return
	}
}

func loadKubeconfig() *rest.Config {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)

	if err != nil {
		zap.L().Fatal("failed to build kubeconfig", zap.Error(err))
	}

	return config
}

func domainRouteIsReady(route *kusciacrd.ClusterDomainRoute) bool {
	for _, condition := range route.Status.Conditions {
		if condition.Type != kusciacrd.ClusterDomainRouteReady {
			continue
		}
		if condition.Status == "True" {
			return true
		}
	}
	return false
}

func run(options ClusterOptions) {
	if _, err := os.Stat(confPath); err == nil {
		zap.S().Info("preserving existing configuration")
	} else {
		config := newKusciaConfig(options)
		ensureParent(confPath)
		if err := os.WriteFile(confPath, config, 0644); err != nil {
			zap.L().Fatal("failed to write config", zap.Error(err))
		}
	}

	ensureParent(stdoutPath)
	stdout, _ := os.Create(stdoutPath)
	stderr, _ := os.Create(stderrPath)

	zap.S().Debug("stdout: ", stdoutPath)
	zap.S().Debug("stderr: ", stderrPath)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)

	started := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		cmd := exec.Command(kusciaPath, "start", "-c", confPath)

		cmd.Stdout = stdout
		cmd.Stderr = stderr

		go func() {
			<-sigs
			zap.S().Info("received SIGINT, stopping...")
			cmd.Process.Signal(os.Interrupt)
		}()

		zap.S().Info("starting control plane...")

		if err := cmd.Start(); err != nil {
			zap.L().Fatal("failed to start control plane", zap.Error(err))
		}

		close(started)

		if err := cmd.Wait(); err != nil {
			zap.L().Fatal("kuscia exited with error", zap.Error(err))
		}
	}()

	<-started

	if err := tryWithTimeout("k3s liveness", probeHTTPEndpoint(k3sHTTP, k3sCertPath), 60, 5); err != nil {
		zap.L().Fatal("k3s is not ready", zap.Error(err))
	} else {
		zap.S().Info("k3s is ready")
	}

	go func() {
		if err := tryWithTimeout("containerd liveness", func() (bool, error) {
			if _, err := net.Dial("unix", containerdPath); err != nil {
				return false, err
			}
			return true, nil
		}, 60, 5); err != nil {
			zap.L().Fatal("timed out waiting for containerd", zap.Error(err))
		}
		for _, appImageFilePath := range options.Images {
			zap.S().Info("loading image using ", appImageFilePath)

			appImageFile, err := os.ReadFile(appImageFilePath)

			if err != nil {
				zap.L().Fatal("failed to read image file", zap.Error(err))
			}

			appImage := kusciacrd.AppImage{}

			if err := yaml2.Unmarshal(appImageFile, &appImage); err != nil {
				zap.L().Fatal("failed to unmarshal image file", zap.Error(err))
			}

			imageFile := appImage.Annotations["load-from"]

			if imageFile == "" {
				zap.L().Fatal("image file not specified")
			}

			imageFile = filepath.Join(filepath.Dir(appImageFilePath), imageFile)

			cmd := exec.Command("ctr", fmt.Sprintf("-a=%s", containerdPath), "images", "import", imageFile, "--no-unpack")
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				zap.L().Fatal("failed to import image", zap.Error(err))
			} else {
				zap.S().Info("image loaded")
			}
		}
	}()

	kubectl := kusciaapi.NewForConfigOrDie(loadKubeconfig())

	if err := tryWithTimeout("if gateways are created", func() (bool, error) {
		gateways, err := kubectl.Gateways(options.SelfDomain).List(context.Background(), v1.ListOptions{})
		if gateways != nil && len(gateways.Items) > 0 {
			return true, nil
		} else {
			return false, err
		}
	}, 60, 5); err != nil {
		zap.L().Fatal("gateways are not created", zap.Error(err))
	} else {
		zap.S().Info("gateways are ready")
	}

	domainCert, err := os.Open(domainCertPath)

	if err != nil {
		zap.L().Fatal("failed to open domain cert file", zap.Error(err))
	}

	defer domainCert.Close()

	publicCertPath := filepath.Join(sharedDataPath, fmt.Sprintf("%s.domain.crt", options.SelfDomain))

	ensureParent(publicCertPath)

	publicCert, err := os.Create(publicCertPath)

	if err != nil {
		zap.L().Fatal("failed to create public cert file", zap.Error(err))
	}

	defer publicCert.Close()

	if _, err := io.Copy(publicCert, domainCert); err != nil {
		zap.L().Fatal("failed to copy domain cert", zap.Error(err))
	}

	zap.S().Info("domain cert copied to public path")

	peerCertPath := filepath.Join(sharedDataPath, fmt.Sprintf("%s.domain.crt", options.PeerDomain))

	peerCertChan := make(chan struct {
		content []byte
		err     error
	})

	zap.S().Info("waiting for peer domain cert to become available")

	go waitForFile(peerCertChan, peerCertPath, 60)

	peerCert := <-peerCertChan

	if peerCert.err != nil {
		zap.S().Fatal("failed to receive cert from peer domain, are they alive? error: ", zap.Error(peerCert.err))
	}

	peerDomain := kusciacrd.Domain{
		ObjectMeta: v1.ObjectMeta{
			Name: options.PeerDomain,
			Annotations: func() map[string]string {
				anno := make(map[string]string)
				anno[options.PeerDomainAsAnnotation()] = "kuscia.secretflow/domain-type=embedded"
				return anno
			}(),
		},
		Spec: kusciacrd.DomainSpec{
			Cert: base64.RawStdEncoding.EncodeToString(peerCert.content),
			Role: kusciacrd.Partner,

			InterConnProtocols: []kusciacrd.InterConnProtocolType{
				kusciacrd.InterConnKuscia,
			},
			AuthCenter: &kusciacrd.AuthCenter{
				AuthenticationType: kusciacrd.DomainAuthenticationToken,
				TokenGenMethod:     kusciacrd.TokenGenMethodRSA,
			},
		},
	}

	if _, err := kubectl.Domains().Create(context.Background(), &peerDomain, v1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			zap.S().Infof("peer domain %s already exists", options.PeerDomain)
		} else {
			zap.S().Fatal("failed to create peer domain", zap.Error(err))
		}
	} else {
		zap.S().Infof("peer domain %s created", options.PeerDomain)
	}

	peerDomainRoute := kusciacrd.ClusterDomainRoute{
		ObjectMeta: v1.ObjectMeta{
			Name: options.DomainRouteAsName(),
		},
		Spec: kusciacrd.ClusterDomainRouteSpec{
			DomainRouteSpec: kusciacrd.DomainRouteSpec{
				Source:      options.SelfDomain,
				Destination: options.PeerDomain,
				Endpoint: kusciacrd.DomainEndpoint{
					Host: options.PeerDomain,
					Ports: []kusciacrd.DomainPort{
						{
							Name:     "http",
							Protocol: kusciacrd.DomainRouteProtocolHTTP,
							Port:     envoyPort,
						},
					},
				},
				InterConnProtocol:  kusciacrd.InterConnKuscia,
				AuthenticationType: kusciacrd.DomainAuthenticationToken,
				TokenConfig: &kusciacrd.TokenConfig{
					TokenGenMethod:      kusciacrd.TokenGenMethodRSA,
					RollingUpdatePeriod: 60,
				},
			},
		},
	}

	if route, err := kubectl.ClusterDomainRoutes().Create(context.Background(), &peerDomainRoute, v1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			zap.S().Infof("peer domain route %s already exists", options.DomainRouteAsName())
			route, _ := kubectl.ClusterDomainRoutes().Get(context.Background(), options.DomainRouteAsName(), v1.GetOptions{})
			if !domainRouteIsReady(route) {
				zap.S().Fatal("existing domain route is not ready")
			}
		} else {
			zap.S().Fatal("failed to create domain route to peer", zap.Error(err))
		}
	} else {
		zap.S().Infof("peer domain route %s created", route.GetName())
		zap.S().Info("waiting for route to become ready")

		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()

		watch, err := kubectl.ClusterDomainRoutes().Watch(ctx, v1.ListOptions{})
		if err != nil {
			zap.S().Fatal("failed to watch domain routes", zap.Error(err))
		}

		func() {
			for {
				select {
				case event, ok := <-watch.ResultChan():
					if !ok {
						zap.S().Fatal("failed to wait for domain route to become ready")
					}
					route := event.Object.(*kusciacrd.ClusterDomainRoute)
					if route.GetName() != options.DomainRouteAsName() {
						continue
					}
					if !domainRouteIsReady(route) {
						zap.S().Debug("domain route is not ready yet")
						continue
					}
					zap.S().Info("domain route is ready")
					return
				case <-ctx.Done():
					zap.S().Fatal("timed out waiting for domain route to become ready")
				}
			}
		}()
	}

	<-stopped
}

func newEncodedRSAKey() string {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		zap.S().Fatal("failed to generate RSA key", zap.Error(err))
	}
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})
	base64EncodedKey := base64.StdEncoding.EncodeToString(privateKeyPEM)
	return base64EncodedKey
}
