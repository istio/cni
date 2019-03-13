package main

import (
	"istio.io/cni/pkg/istioproxyagent/server"
	"os"
)

func main() {

	bindAddr := os.Args[1]
	agent, err := server.NewProxyAgent(bindAddr)
	if err != nil {
		panic(err)
	}

	err = agent.Run()
	if err != nil {
		panic(err)
	}

}
