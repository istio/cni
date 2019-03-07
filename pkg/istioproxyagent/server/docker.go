package server

import (
	"github.com/sirupsen/logrus"
	"istio.io/cni/pkg/istioproxyagent/api"
	"os/exec"
	"strconv"
)

type DockerRuntime struct {
	logger *logrus.Entry
}

func NewDockerRuntime() (*DockerRuntime, error) {
	return &DockerRuntime{
		logger: logrus.NewEntry(logrus.New()),
	}, nil
}

func (p *DockerRuntime) StartProxy(request *api.StartRequest) error {
	config := NewDefaultProxyConfig() // TODO: get this from configmap (or from client?)

	secretDir, confDir, err := createVolumes()
	if err != nil {
		return err
	}

	p.logger.Info("Writing secret...")
	err = writeSecret(secretDir, request.SecretData)
	if err != nil {
		return err
	}
	p.logger.Infof("Secret written to %s", secretDir)

	annotationsJSON, err := toJSON(request.Annotations)
	if err != nil {
		return err
	}

	labelsJSON, err := toJSON(request.Labels)
	if err != nil {
		return err
	}

	args := []string{
		"run",
		"-d",
		"--name",
		getContainerName(request.PodName),
		"--user",
		strconv.FormatInt(config.runAsUser, 10),
		"--net",
		"container:" + request.PodSandboxID,
		// TODO: set other namespaces
		"-e",
		"POD_NAME=" + request.PodName,
		"-e",
		"POD_NAMESPACE=" + request.PodNamespace,
		"-e",
		"INSTANCE_IP=" + request.PodIP,
		"-e",
		"ISTIO_META_POD_NAME=" + request.PodName,
		"-e",
		"ISTIO_META_INTERCEPTION_MODE=" + config.interceptionMode,
		"-e",
		//"ISTIO_METAJSON_ANNOTATIONS=" + `{"openshift.io/scc":"anyuid","sidecar.istio.io/inject":"true"}`, // TODO: get annotations from pod
		"ISTIO_METAJSON_ANNOTATIONS=" + annotationsJSON,
		"-e",
		//"ISTIO_METAJSON_LABELS=" + `{"app":"details","pod-template-hash":"1062614857","version":"v1"}`,   // TODO: get labels from pod
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

func (p *DockerRuntime) StopProxy(request *api.StopRequest) error {
	return p.runDockerCommand([]string{"stop", getContainerName(request.PodName)})
}

func (p *DockerRuntime) IsReady(request *api.ReadinessRequest) (bool, error) {
	return true, nil
}

func getContainerName(podName string) string {
	return podName + "-" + "istio-proxy"
}

func (p *DockerRuntime) runDockerCommand(args []string) error {
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

type ProxyConfig struct {
	image            string
	args             []string
	runAsUser        int64
	interceptionMode string
}

func NewDefaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		image: "docker.io/istio/proxyv2:1.1.0-rc.2",
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
			"--concurrency",
			"2",
			"--controlPlaneAuthPolicy",
			"NONE",
			"--statusPort",
			"15020",
			"--applicationPorts",
			"9080",
		},
		runAsUser:        1337,
		interceptionMode: "REDIRECT",
	}
}
