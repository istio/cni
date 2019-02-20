package main

import (
	"encoding/json"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"time"
)

type ProxyConfig struct {
	image            string
	args             []string
	runAsUser        string
	interceptionMode string
}

func NewDefaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		image: "maistra/proxyv2-centos7:0.8.0",
		args: []string{
			"proxy",
			"sidecar",
			"--domain",
			"myproject.svc.cluster.local",
			"--configPath",
			"/etc/istio/proxy",
			"--binaryPath",
			"/usr/local/bin/envoy",
			"--serviceCluster",
			"details.myproject",
			"--drainDuration",
			"45s",
			"--parentShutdownDuration",
			"1m0s",
			"--discoveryAddress",
			"istio-pilot.istio-system:15010",
			"--zipkinAddress",
			"zipkin.istio-system:9411",
			"--connectTimeout",
			"10s",
			"--proxyAdminPort",
			"15000",
			"--controlPlaneAuthPolicy",
			"NONE",
			"--statusPort",
			"15020",
			"--applicationPorts",
			"9080",
			"--concurrency",
			"1",
		},
		runAsUser:        "1337",
		interceptionMode: "REDIRECT",
	}
}

type Proxy struct {
	logger *logrus.Entry
}

func NewProxy(logger *logrus.Entry) (*Proxy, error) {
	return &Proxy{
		logger: logger,
	}, nil
}

func (p *Proxy) runProxy(podName, podNamespace, podIP, infraContainerID string, secretData map[string][]byte, labels, annotations map[string]string) error {
	// TODO: rewrite this so it uses CRI

	config := NewDefaultProxyConfig()

	secretDir, confDir, err := p.createVolumes()
	if err != nil {
		return err
	}

	p.logger.Info("Writing secret...")
	err = p.writeSecret(secretDir, secretData)
	if err != nil {
		return err
	}
	p.logger.Infof("Secret written to %s", secretDir)

	annotationsJSON, err := toJSON(annotations)
	if err != nil {
		return err
	}

	labelsJSON, err := toJSON(labels)
	if err != nil {
		return err
	}

	args := []string{
		"run",
		"-d",
		"--name",
		getContainerName(podName),
		"--user",
		config.runAsUser,
		"--net",
		"container:" + infraContainerID,
		// TODO: set other namespaces
		"-e",
		"POD_NAME=" + podName,
		"-e",
		"POD_NAMESPACE=" + podNamespace,
		"-e",
		"INSTANCE_IP=" + podIP,
		"-e",
		"ISTIO_META_POD_NAME=" + podName,
		"-e",
		"ISTIO_META_CONFIG_NAMESPACE=" + podNamespace,
		"-e",
		"ISTIO_META_INTERCEPTION_MODE=" + config.interceptionMode,
		"-e",
		"ISTIO_METAJSON_ANNOTATIONS=" + annotationsJSON,
		"-e",
		"ISTIO_METAJSON_LABELS=" + labelsJSON,

		//"--tmpfs",
		//"/etc/istio/proxy:rw,mode=1777",	// mode is ignored, so we can't use tmpfs
		//"--mount",
		//"type=tmpfs,destination=/etc/istio/proxy/,tmpfs-mode=1777,rw",	// --mount not supported on some Docker versions
		"-v",
		confDir + ":" + "/etc/istio/proxy/" + ":rw",

		"-v",
		secretDir + ":" + "/etc/certs/" + ":ro",

		config.image,
	}
	args = append(args, config.args...)

	return p.runDockerCommand(args)
}

func (p *Proxy) stopProxy(podName string) error {
	return p.runDockerCommand([]string{"stop", getContainerName(podName)})
}

func getContainerName(podName string) string {
	return podName + "-" + "istio-proxy"
}

func (p *Proxy) runDockerCommand(args []string) error {
	p.logger.Infof("Running docker with: %v", args)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"out": string(out[:]),
			"err": err,
		}).Errorf("docker failed: %v", err)
		p.logger.Infof("docker out: %s", out)
	} else {
		p.logger.Infof("docker done: %s", out)
	}
	return err
}

func toJSON(obj interface{}) (string, error) {
	bytes, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func (p *Proxy) createVolumes() (string, string, error) {
	random := rand.New(rand.NewSource(time.Now().UnixNano()))
	dir := "/tmp/istio-proxy-volumes-" + strconv.Itoa(random.Int())
	p.logger.Infof("Creating dir %s", dir)
	certsDir := dir + "/certs"
	err := os.MkdirAll(certsDir, os.ModePerm)
	if err != nil {
		return "", "", err
	}

	confDir := dir + "/conf"
	err = os.Mkdir(confDir, os.ModePerm)
	if err != nil {
		return "", "", err
	}

	// ensure the conf dir is world writable (might not be if umask is set)
	err = os.Chmod(confDir, 0777)
	if err != nil {
		return "", "", err
	}

	return certsDir, confDir, nil
}

func (p *Proxy) writeSecret(dir string, secretData map[string][]byte) error {
	p.logger.Info("Writing secret data")
	for k, v := range secretData {
		p.logger.Infof("Writing secret file %s", k)
		err := ioutil.WriteFile(dir+"/"+k, v, os.ModePerm)
		if err != nil {
			return err
		}
	}
	return nil
}
