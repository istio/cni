// Copyright 2018 Istio authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Defines the redirect object and operations.
package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	redirectModeREDIRECT         = "REDIRECT"
	redirectModeTPROXY           = "TPROXY"
	defaultProxyStatusPort       = "15020"
	defaultRedirectToPort        = "15001"
	defaultNoRedirectUID         = "1337"
	defaultRedirectMode          = redirectModeREDIRECT
	defaultRedirectIPCidr        = "*"
	defaultRedirectExcludeIPCidr = ""
	defaultRedirectExcludePort   = defaultProxyStatusPort
	defaultKubevirtInterfaces    = ""

	includeIPCidrsKey       = "traffic.sidecar.istio.io/includeOutboundIPRanges"
	excludeIPCidrsKey       = "traffic.sidecar.istio.io/excludeOutboundIPRanges"
	includePortsKey         = "traffic.sidecar.istio.io/includeInboundPorts"
	excludeInboundPortsKey  = "traffic.sidecar.istio.io/excludeInboundPorts"
	excludeOutboundPortsKey = "traffic.sidecar.istio.io/excludeOutboundPorts"

	sidecarInterceptModeKey = "sidecar.istio.io/interceptionMode"
	sidecarPortListKey      = "status.sidecar.istio.io/port"

	kubevirtInterfacesKey = "traffic.sidecar.istio.io/kubevirtInterfaces"
)

var (
	annotationRegistry = map[string]*annotationParam{
		"inject":               {injectAnnotationKey, "", alwaysValidFunc},
		"status":               {sidecarStatusKey, "", alwaysValidFunc},
		"redirectMode":         {sidecarInterceptModeKey, defaultRedirectMode, validateInterceptionMode},
		"ports":                {sidecarPortListKey, "", validatePortList},
		"includeIPCidrs":       {includeIPCidrsKey, defaultRedirectIPCidr, validateCIDRListWithWildcard},
		"excludeIPCidrs":       {excludeIPCidrsKey, defaultRedirectExcludeIPCidr, validateCIDRList},
		"includePorts":         {includePortsKey, "", validatePortListWithWildcard},
		"excludeInboundPorts":  {excludeInboundPortsKey, defaultRedirectExcludePort, validatePortList},
		"excludeOutboundPorts": {excludeOutboundPortsKey, defaultRedirectExcludePort, validatePortList},
		"kubevirtInterfaces":   {kubevirtInterfacesKey, defaultKubevirtInterfaces, alwaysValidFunc},
	}
)

// InterceptRuleMgr configures networking tables (e.g. iptables or nftables) for redirecting traffic to an Istio proxy.
type InterceptRuleMgr interface {
	Program(netns string, redirect *Redirect) error
}

// Redirect -- the istio-cni redirect object
type Redirect struct {
	targetPort           string
	redirectMode         string
	noRedirectUID        string
	includeIPCidrs       string
	includePorts         string
	excludeIPCidrs       string
	excludeInboundPorts  string
	excludeOutboundPorts string
	kubevirtInterfaces   string

	logger *logrus.Entry
}

type annotationValidationFunc func(value string) error

type annotationParam struct {
	key        string
	defaultVal string
	validator  annotationValidationFunc
}

func alwaysValidFunc(value string) error {
	return nil
}

// validateInterceptionMode validates the interceptionMode annotation
func validateInterceptionMode(mode string) error {
	switch mode {
	case redirectModeREDIRECT:
	case redirectModeTPROXY:
	default:
		return fmt.Errorf("interceptionMode invalid: %v", mode)
	}
	return nil
}

func validateCIDRList(cidrs string) error {
	if len(cidrs) > 0 {
		for _, cidr := range strings.Split(cidrs, ",") {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("failed parsing cidr '%s': %v", cidr, err)
			}
		}
	}
	return nil
}

func splitPorts(portsString string) []string {
	return strings.Split(portsString, ",")
}

func parsePort(portStr string) (uint16, error) {
	port, err := strconv.ParseUint(strings.TrimSpace(portStr), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("failed parsing port %q: %v", portStr, err)
	}
	return uint16(port), nil
}

func parsePorts(portsString string) ([]int, error) {
	portsString = strings.TrimSpace(portsString)
	ports := make([]int, 0)
	if len(portsString) > 0 {
		for _, portStr := range splitPorts(portsString) {
			port, err := parsePort(portStr)
			if err != nil {
				return nil, err
			}
			ports = append(ports, int(port))
		}
	}
	return ports, nil
}

func validatePortList(ports string) error {
	if _, err := parsePorts(ports); err != nil {
		return fmt.Errorf("portList %q invalid: %v", ports, err)
	}
	return nil
}

func validatePortListWithWildcard(ports string) error {
	if ports != "*" {
		return validatePortList(ports)
	}
	return nil
}

// ValidateIncludeIPRanges validates the includeIPRanges parameter
func validateCIDRListWithWildcard(ipRanges string) error {
	if ipRanges != "*" {
		if e := validateCIDRList(ipRanges); e != nil {
			return fmt.Errorf("IPRanges invalid: %v", e)
		}
	}
	return nil
}

func getAnnotationOrDefault(name string, annotations map[string]string) (isFound bool, val string, err error) {
	if _, ok := annotationRegistry[name]; !ok {
		return false, "", fmt.Errorf("no registered annotation with name=%s", name)
	}
	// use annotation value if present
	if val, found := annotations[annotationRegistry[name].key]; found {
		if err := annotationRegistry[name].validator(val); err != nil {
			return true, annotationRegistry[name].defaultVal, err
		}
		return true, val, nil
	}
	// no annotation found so use default value
	return false, annotationRegistry[name].defaultVal, nil
}

// NewRedirect returns a new Redirect Object constructed from a list of ports and annotations
func NewRedirect(ports []string, annotations map[string]string, logger *logrus.Entry) (*Redirect, error) {
	var isFound bool
	var valErr error

	redir := &Redirect{}
	redir.logger = logger
	redir.targetPort = defaultRedirectToPort
	isFound, redir.redirectMode, valErr = getAnnotationOrDefault("redirectMode", annotations)
	if valErr != nil {
		logger.Errorf("Annotation value error for value %s; annotationFound = %t: %v",
			"redirectMode", isFound, valErr)
		return nil, valErr
	}
	redir.noRedirectUID = defaultNoRedirectUID
	isFound, redir.includeIPCidrs, valErr = getAnnotationOrDefault("includeIPCidrs", annotations)
	if valErr != nil {
		logger.Errorf("Annotation value error for value %s; annotationFound = %t: %v",
			"includeIPCidrs", isFound, valErr)
		return nil, valErr
	}
	isFound, redir.includePorts, valErr = getAnnotationOrDefault("includePorts", annotations)
	if !isFound || valErr != nil {
		redir.includePorts = strings.Join(ports, ",")
		if valErr != nil {
			logger.Errorf("Annotation value error for redirect ports, using ContainerPorts=\"%s\": %v",
				redir.includePorts, valErr)
			return nil, valErr
		}
	}
	isFound, redir.excludeIPCidrs, valErr = getAnnotationOrDefault("excludeIPCidrs", annotations)
	if valErr != nil {
		logger.Errorf("Annotation value error for value %s; annotationFound = %t: %v",
			"excludeIPCidrs", isFound, valErr)
		return nil, valErr
	}
	isFound, redir.excludeInboundPorts, valErr = getAnnotationOrDefault("excludeInboundPorts", annotations)
	if valErr != nil {
		logger.Errorf("Annotation value error for value %s; annotationFound = %t: %v",
			"excludeInboundPorts", isFound, valErr)
		return nil, valErr
	}
	isFound, redir.excludeOutboundPorts, valErr = getAnnotationOrDefault("excludeOutboundPorts", annotations)
	if valErr != nil {
		logger.Errorf("Annotation value error for value %s; annotationFound = %t: %v",
			"excludeOutboundPorts", isFound, valErr)
		return nil, valErr
	}
	isFound, redir.kubevirtInterfaces, valErr = getAnnotationOrDefault("kubevirtInterfaces", annotations)
	if valErr != nil {
		logger.Errorf("Annotation value error for value %s; annotationFound = %t: %v",
			"kubevirtInterfaces", isFound, valErr)
		return nil, valErr
	}

	return redir, nil
}
