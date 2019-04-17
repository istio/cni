package main

import (
	"context"
	"flag"
	"istio.io/cni/pkg/istioproxyagent/server"
)

func main() {

	config := server.ProxyAgentConfig{}

	flag.StringVar(&config.BindAddr, "bind-addr", ":22222", "Address to bind to for serving")
	flag.StringVar(&config.ControlPlaneNamespace, "control-plane-namespace", "istio-system", "Namespace where Istio control plane is running")
	flag.StringVar(&config.MeshConfigMapName, "mesh-configmap-name", "istio", "Name of ConfigMap holding the mesh config")
	flag.StringVar(&config.MeshConfigMapKey, "mesh-configmap-key", "mesh", "Key in the mesh ConfigMap that holds the mesh config")
	flag.StringVar(&config.InjectConfigMapName, "injector-configmap-name", "istio-sidecar-injector", "Name of sidecar injector ConfigMap")
	flag.StringVar(&config.InjectConfigMapKey, "injector-configmap-key", "config", "Key in the injector ConfigMap that holds the injector config")

	flag.Parse()

	agent, err := server.NewProxyAgent(config)
	if err != nil {
		panic(err)
	}

	err = agent.Run(context.TODO())
	if err != nil {
		panic(err)
	}

}
