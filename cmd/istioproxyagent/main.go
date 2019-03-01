package main

import (
	"istio.io/cni/pkg/istioproxyagent/server"
)

func main() {

	agent, err := server.NewProxyAgent()
	if err != nil {
		panic(err)
	}

	err = agent.Run()
	if err != nil {
		panic(err)
	}

}
