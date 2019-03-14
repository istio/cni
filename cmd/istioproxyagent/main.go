package main

import (
	"flag"
	"istio.io/cni/pkg/istioproxyagent/server"
)

func main() {

	config := server.ProxyAgentConfig{}

	flag.StringVar(&config.BindAddr, "bind-addr", ":22222", "Address to bind to for serving")
	flag.StringVar(&config.SidecarContainerName, "sidecar-container-name", "istio-proxy", "Name to use for the sidecar container")
	flag.Parse()

	agent, err := server.NewProxyAgent(config)
	if err != nil {
		panic(err)
	}

	err = agent.Run()
	if err != nil {
		panic(err)
	}

}
