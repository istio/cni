// Copyright 2019 Istio Authors
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

// This is a sample chained plugin that supports multiple CNI versions. It
// parses prevResult according to the cniVersion
package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	definitions "github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/sbezverk/nftableslib"
	netnslib "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"go.uber.org/zap"

	"istio.io/pkg/log"
)

type nftables struct {
}

func newNFTables() InterceptRuleMgr {
	return &nftables{}
}

// Program defines a method which programs nftables based on the parameters
// provided in Redirect.
func (nft *nftables) Program(netns string, rdrct *Redirect) error {
	log.Info("nftables",
		zap.String("netns", netns))

	fd, err := netnslib.GetFromPath(netns)
	if err != nil {
		log.Error("failed to get netns fd with error",
			zap.Error(err))
		return err
	}

	log.Info("nftables arguments",
		zap.String("netns", netns),
		zap.String("fd", string(fd)))
	// Initializing netlink connection
	conn := nftableslib.InitConn(int(fd))
	ti := nftableslib.InitNFTables(conn)
	// Cleaning up tables/chains/rules
	conn.FlushRuleset()

	// Setting up common tables and chains for ipv4 and ipv6
	ipv4ci, ipv6ci, err := setupTablesChains(rdrct, ti)
	if err != nil {
		return err
	}

	ipv4Out, ipv6Out, err := generateOutboundRules(rdrct)
	if err != nil {
		return err
	}

	ipv4ri, err := ipv4ci.Chains().Chain("istio_outbound_ipv4")
	if err != nil {
		return nil
	}
	if err := setupRules(ipv4ri, ipv4Out); err != nil {
		return err
	}
	ipv6ri, err := ipv6ci.Chains().Chain("istio_outbound_ipv6")
	if err != nil {
		return nil
	}
	if err := setupRules(ipv6ri, ipv6Out); err != nil {
		return err
	}

	ipv4In, ipv6In, err := generateInboundRules(rdrct)
	if err != nil {
		return err
	}
	ipv4ri, err = ipv4ci.Chains().Chain("istio_inbound_ipv4")
	if err != nil {
		return nil
	}
	if err := setupRules(ipv4ri, ipv4In); err != nil {
		return err
	}
	ipv6ri, err = ipv6ci.Chains().Chain("istio_inbound_ipv6")
	if err != nil {
		return nil
	}
	if err := setupRules(ipv6ri, ipv6In); err != nil {
		return err
	}
	// At this point all preparation is done, time to redirect traffic, starting with
	// outgoing traffic, then following with incoming traffic.
	if err := switchTraffic(ipv4ci, ipv6ci); err != nil {
		return err
	}
	// Get json like representation of all rules and print it for debugging purposes
	r, _ := ti.Tables().Dump()
	log.Info("nftables resulting rules",
		zap.String("fd", string(r)))

	return nil
}

func switchTraffic(ipv4ci nftableslib.ChainsInterface, ipv6ci nftableslib.ChainsInterface) error {
	tcpProto := uint32(unix.IPPROTO_TCP)
	ra1ipv4, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_outbound_ipv4")
	ipv4OutTraffic := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Protocol: &tcpProto,
		},
		Action: ra1ipv4,
	}
	ra1ipv6, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_outbound_ipv6")
	ipv6OutTraffic := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Protocol: &tcpProto,
		},
		Action: ra1ipv6,
	}
	ipv4ch1ri, err := ipv4ci.Chains().Chain("istio_output_ipv4")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_output_ipv4 with error",
			zap.Error(err))
		return err
	}
	ipv6ch1ri, err := ipv6ci.Chains().Chain("istio_output_ipv6")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_output_ipv6 with error",
			zap.Error(err))
		return err
	}
	if _, err := ipv4ch1ri.Rules().CreateImm(&ipv4OutTraffic); err != nil {
		log.Error("Failed to create the redirect rule for chain istio_output_ipv4 with error",
			zap.Error(err))
		return err
	}
	if _, err := ipv6ch1ri.Rules().CreateImm(&ipv6OutTraffic); err != nil {
		log.Error("Failed to create the redirect rule for chain istio_output_ipv6 with error",
			zap.Error(err))
		return err
	}
	ra2ipv4, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_inbound_ipv4")
	ipv4InTraffic := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Protocol: &tcpProto,
		},
		Action: ra2ipv4,
	}
	ra2ipv6, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_inbound_ipv6")
	ipv6InTraffic := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Protocol: &tcpProto,
		},
		Action: ra2ipv6,
	}
	ipv4ch2ri, err := ipv4ci.Chains().Chain("istio_prerouting_ipv4")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_prerouting_ipv4 with error",
			zap.Error(err))
		return err
	}
	ipv6ch2ri, err := ipv6ci.Chains().Chain("istio_prerouting_ipv6")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_prerouting_ipv6 with error",
			zap.Error(err))
		return err
	}
	if _, err := ipv4ch2ri.Rules().CreateImm(&ipv4InTraffic); err != nil {
		log.Error("Failed to create the redirect rule for chain istio_prerouting_ipv4 with error",
			zap.Error(err))
		return err
	}
	if _, err := ipv6ch2ri.Rules().CreateImm(&ipv6InTraffic); err != nil {
		log.Error("Failed to create the redirect rule for chain istio_prerouting_ipv6 with error",
			zap.Error(err))
		return err
	}

	return nil
}

func setupRules(ri nftableslib.RulesInterface, rules []*nftableslib.Rule) error {
	for _, rule := range rules {
		if _, err := ri.Rules().CreateImm(rule); err != nil {
			return err
		}
	}
	return nil
}

// setupTablesChains programs nf tables for ipv4 and ipv6 and all required
// chains. istio_redirect chain is common for inbound and outbound. Common exclusions are
// also programmed in this func.
func setupTablesChains(rdrct *Redirect, ti nftableslib.TablesInterface) (nftableslib.ChainsInterface, nftableslib.ChainsInterface, error) {
	// Creating ipv4 table which hosts ipv4 related chains and rules
	if err := ti.Tables().CreateImm("istio_ipv4", definitions.TableFamilyIPv4); err != nil {
		log.Error("Failed to create table istio-ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	// Creating ipv6 table which hosts ipv6 related chains and rules
	if err := ti.Tables().CreateImm("istio_ipv6", definitions.TableFamilyIPv6); err != nil {
		log.Error("Failed to create table istio-ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	// Getting Chain interfaces from ipv4 and ipv6 tables
	ipv4ci, err := ti.Tables().Table("istio_ipv4", definitions.TableFamilyIPv4)
	if err != nil {
		log.Error("Failed to get chains interface for table istio_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	ipv6ci, err := ti.Tables().Table("istio_ipv6", definitions.TableFamilyIPv6)
	if err != nil {
		log.Error("Failed to get chains interface for table istio_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	// Defining 2 base chains preruting and output
	preRoutingAttr := nftableslib.ChainAttributes{
		Type:     definitions.ChainTypeNAT,
		Priority: 0,
		Hook:     definitions.ChainHookPrerouting,
	}
	outputAttr := nftableslib.ChainAttributes{
		Type:     definitions.ChainTypeNAT,
		Priority: 0,
		Hook:     definitions.ChainHookOutput,
	}
	// istioInitialChains defines a list of common base and regular chains
	istioInitialChains := []struct {
		family definitions.TableFamily
		name   string
		attr   *nftableslib.ChainAttributes
	}{
		// ipv4 base chains for managing ipv4 related rules
		{
			family: definitions.TableFamilyIPv4,
			name:   "istio_prerouting_ipv4",
			attr:   &preRoutingAttr,
		},
		{
			family: definitions.TableFamilyIPv4,
			name:   "istio_output_ipv4",
			attr:   &outputAttr,
		},
		// ipv6 base chains for managing ipv6 related rules
		{
			family: definitions.TableFamilyIPv6,
			name:   "istio_prerouting_ipv6",
			attr:   &preRoutingAttr,
		},
		{
			family: definitions.TableFamilyIPv6,
			name:   "istio_output_ipv6",
			attr:   &outputAttr,
		},
		// ipv4 regular chains for managing ipv4 related rules
		{
			family: definitions.TableFamilyIPv4,
			name:   "istio_inbound_ipv4",
			attr:   nil,
		},
		{
			family: definitions.TableFamilyIPv4,
			name:   "istio_outbound_ipv4",
			attr:   nil,
		},
		{
			family: definitions.TableFamilyIPv4,
			name:   "istio_redirect_ipv4",
			attr:   nil,
		},
		// ipv6 regular chains for managing ipv6 related rules
		{
			family: definitions.TableFamilyIPv6,
			name:   "istio_inbound_ipv6",
			attr:   nil,
		},
		{
			family: definitions.TableFamilyIPv6,
			name:   "istio_outbound_ipv6",
			attr:   nil,
		},
		{
			family: definitions.TableFamilyIPv6,
			name:   "istio_redirect_ipv6",
			attr:   nil,
		},
	}

	for _, ch := range istioInitialChains {
		switch {
		case ch.family == definitions.TableFamilyIPv4:
			if err := ipv4ci.Chains().CreateImm(ch.name, ch.attr); err != nil {
				log.Error("Failed to create",
					zap.String("ipv4 chain", ch.name),
					zap.Error(err))
				return nil, nil, err
			}
		case ch.family == definitions.TableFamilyIPv6:
			if err := ipv6ci.Chains().CreateImm(ch.name, ch.attr); err != nil {
				log.Error("Failed to create",
					zap.String("ipv6 chain", ch.name),
					zap.Error(err))
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("unknown table family type %+v for chain %s", ch.family, ch.name)

		}
	}

	proxyPort, err := strconv.Atoi(rdrct.targetPort)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid value of a target port %s, failed to convert to int with error: %+v", rdrct.targetPort, err)
	}
	tcpProto := uint32(unix.IPPROTO_TCP)
	ra1, _ := nftableslib.SetRedirect(proxyPort, false)
	redirectRule := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Protocol: &tcpProto,
		},
		Action: ra1,
	}
	ipv4ch3ri, err := ipv4ci.Chains().Chain("istio_redirect_ipv4")
	if err != nil {
		log.Error("Failed to get rules interface for chain  istio_redirect_ipv4 with erro",
			zap.Error(err))
		return nil, nil, err
	}
	ipv6ch3ri, err := ipv6ci.Chains().Chain("istio_redirect_ipv6")
	if err != nil {
		log.Error("Failed to get rules interface for chain  istio_redirect_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv4ch3ri.Rules().CreateImm(&redirectRule); err != nil {
		log.Error("Failed to create the redirect rule for chain  istio_redirect_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv6ch3ri.Rules().CreateImm(&redirectRule); err != nil {
		log.Error("Failed to create the redirect rule for chain  istio_redirect_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	raRet, _ := nftableslib.SetVerdict(unix.NFT_RETURN)
	sshPort := uint16(22)
	list := []*uint16{&sshPort}
	sshExclRule := nftableslib.Rule{
		L4: &nftableslib.L4Rule{
			L4Proto: unix.IPPROTO_TCP,
			Dst: &nftableslib.Port{
				List: list,
			},
		},
		Action: raRet,
	}
	ipv4ch2ri, err := ipv4ci.Chains().Chain("istio_inbound_ipv4")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_inbound_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	ipv6ch2ri, err := ipv6ci.Chains().Chain("istio_inbound_ipv6")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_inbound_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv4ch2ri.Rules().CreateImm(&sshExclRule); err != nil {
		log.Error("Failed to create ssh exclude rule for chain istio_inbound_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv6ch2ri.Rules().CreateImm(&sshExclRule); err != nil {
		log.Error("Failed to create ssh exclude rule for chain istio_inbound_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	lbipv4, _ := nftableslib.NewIPAddr("127.0.0.1/32")
	// Exclude traffic destined to loopback
	ipv4ExclLoop := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Dst: &nftableslib.IPAddrSpec{
				List: []*nftableslib.IPAddr{lbipv4},
			},
		},
		Action: raRet,
	}
	lbipv6, _ := nftableslib.NewIPAddr("::1/128")
	ipv6ExclLoop := nftableslib.Rule{
		L3: &nftableslib.L3Rule{
			Dst: &nftableslib.IPAddrSpec{
				List: []*nftableslib.IPAddr{lbipv6},
			},
		},
		Action: raRet,
	}
	ipv4ch4ri, err := ipv4ci.Chains().Chain("istio_outbound_ipv4")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_outbound_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	ipv6ch4ri, err := ipv6ci.Chains().Chain("istio_outbound_ipv6")
	if err != nil {
		log.Error("Failed to get rules interface for chain istio_outbound_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv4ch4ri.Rules().CreateImm(&ipv4ExclLoop); err != nil {
		log.Error("Failed to create istio_loopback_excl_ipv4 rule for chain istio_outbound_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv6ch4ri.Rules().CreateImm(&ipv6ExclLoop); err != nil {
		log.Error("Failed to create istio_loopback_excl_ipv6 rule for chain istio_outbound_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}

	uid, err := strconv.Atoi(rdrct.noRedirectUID)
	if err != nil {
		log.Error("Failed to convert noRedirectUID with error",
			zap.Error(err))
		return nil, nil, err
	}

	uidExcl := nftableslib.Rule{
		Meta: &nftableslib.Meta{
			Expr: []nftableslib.MetaExpr{
				{
					Key:   unix.NFT_META_SKUID,
					Value: binaryutil.NativeEndian.PutUint32(uint32(uid)),
				},
			},
		},
		Action: raRet,
	}
	gidExcl := nftableslib.Rule{
		Meta: &nftableslib.Meta{
			Expr: []nftableslib.MetaExpr{
				{
					Key:   unix.NFT_META_SKGID,
					Value: binaryutil.NativeEndian.PutUint32(uint32(uid)),
				},
			},
		},
		Action: raRet,
	}
	if _, err := ipv4ch4ri.Rules().CreateImm(&uidExcl); err != nil {
		log.Error("Failed to create the istio_proxy_uid_excl_ipv4 rule for chain istio_outbound_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv4ch4ri.Rules().CreateImm(&gidExcl); err != nil {
		log.Error("Failed to create the istio_proxy_gid_excl_ipv4 rule for chain istio_outbound_ipv4 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv6ch4ri.Rules().CreateImm(&uidExcl); err != nil {
		log.Error("Failed to create the istio_proxy_uid_excl_ipv6 rule for chain istio_outbound_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}
	if _, err := ipv6ch4ri.Rules().CreateImm(&gidExcl); err != nil {
		log.Error("Failed to create the istio_proxy_gid_excl_ipv6 rule for chain istio_outbound_ipv6 with error",
			zap.Error(err))
		return nil, nil, err
	}

	return ipv4ci, ipv6ci, nil
}

func generateOutboundRules(rdrct *Redirect) ([]*nftableslib.Rule, []*nftableslib.Rule, error) {
	ipv4 := make([]*nftableslib.Rule, 0)
	ipv6 := make([]*nftableslib.Rule, 0)

	if rdrct.excludeOutboundPorts != "" {
		exclPorts, err := generateOutboundExlcPorts(rdrct.excludeOutboundPorts)
		if err != nil {
			return nil, nil, err
		}
		ipv4 = append(ipv4, exclPorts)
		ipv6 = append(ipv6, exclPorts)
	}
	var ipv4Cidrs, ipv6Cidrs []*nftableslib.Rule
	var err error
	if rdrct.includeIPCidrs != "*" && rdrct.includeIPCidrs != "" {
		// Process include CIDRs and ignore rdrct.excludeIPCidrs
		if ipv4Cidrs, ipv6Cidrs, err = processIncludeCidrs(rdrct.includeIPCidrs); err != nil {
			return nil, nil, err
		}
		ipv4 = append(ipv4, ipv4Cidrs...)
		ipv6 = append(ipv6, ipv6Cidrs...)
	} else {
		// Default is to redirect all CIDRs, then need to check rdrct.excludeIPCidrs
		// for exclusions.
		if ipv4Cidrs, ipv6Cidrs, err = processExcludeCidrs(rdrct.excludeIPCidrs); err != nil {
			return nil, nil, err
		}
		ipv4 = append(ipv4, ipv4Cidrs...)
		ipv6 = append(ipv6, ipv6Cidrs...)
		raipv4, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_redirect_ipv4")
		ipv4 = append(ipv4, &nftableslib.Rule{
			Action: raipv4,
		})
		raipv6, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_redirect_ipv6")
		ipv6 = append(ipv6, &nftableslib.Rule{
			Action: raipv6,
		})
	}

	return ipv4, ipv6, nil
}

func generateInboundRules(rdrct *Redirect) ([]*nftableslib.Rule, []*nftableslib.Rule, error) {
	ipv4 := make([]*nftableslib.Rule, 0)
	ipv6 := make([]*nftableslib.Rule, 0)

	if rdrct.includePorts != "*" && rdrct.includePorts != "" {
		// Process include Ports and ignore rdrct.excludeInboundPorts
		ipv4InclPorts, err := generateInboundInclPorts(rdrct.includePorts, "istio_redirect_ipv4")
		if err != nil {
			return nil, nil, err
		}
		ipv6InclPorts, err := generateInboundInclPorts(rdrct.includePorts, "istio_redirect_ipv6")
		if err != nil {
			return nil, nil, err
		}
		ipv4 = append(ipv4, ipv4InclPorts)
		ipv6 = append(ipv6, ipv6InclPorts)
	} else {
		// Default is to redirect all Ports, then need to check rdrct.excludeInboundPorts
		// for exclusions.
		// Process include Ports and ignore rdrct.excludeInboundPorts
		exclPorts, err := generateInboundExclPorts(rdrct.excludeInboundPorts)
		if err != nil {
			return nil, nil, err
		}
		ipv4 = append(ipv4, exclPorts)
		ipv6 = append(ipv6, exclPorts)
		raipv4, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_redirect_ipv4")
		raipv6, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_redirect_ipv6")
		ipv4 = append(ipv4, &nftableslib.Rule{
			Action: raipv4,
		})
		ipv6 = append(ipv6, &nftableslib.Rule{
			Action: raipv6,
		})
	}

	return ipv4, ipv6, nil
}

func generateInboundInclPorts(includePorts string, chain string) (*nftableslib.Rule, error) {
	list := make([]*uint16, 0)
	ports := strings.Split(includePorts, ",")
	for _, port := range ports {
		p, err := strconv.Atoi(port)
		if err != nil {
			return nil, err
		}
		if p > 65535 {
			return nil, fmt.Errorf("invalid port value of %d", p)
		}
		pp := uint16(p)
		list = append(list, &pp)
	}
	ra, _ := nftableslib.SetVerdict(unix.NFT_GOTO, chain)
	return &nftableslib.Rule{
		L4: &nftableslib.L4Rule{
			L4Proto: unix.IPPROTO_TCP,
			Dst: &nftableslib.Port{
				List: list,
			},
		},
		Action: ra,
	}, nil
}

func generateInboundExclPorts(excludeInboundPorts string) (*nftableslib.Rule, error) {
	list := make([]*uint16, 0)
	ports := strings.Split(excludeInboundPorts, ",")
	for _, port := range ports {
		p, err := strconv.Atoi(port)
		if err != nil {
			return nil, err
		}
		if p > 65535 {
			return nil, fmt.Errorf("invalid port value of %d", p)
		}
		pp := uint16(p)
		list = append(list, &pp)
	}
	ra, _ := nftableslib.SetVerdict(unix.NFT_RETURN)
	return &nftableslib.Rule{
		L4: &nftableslib.L4Rule{
			L4Proto: unix.IPPROTO_TCP,
			Dst: &nftableslib.Port{
				List: list,
			},
		},
		Action: ra,
	}, nil
}

func processIncludeCidrs(cidrs string) ([]*nftableslib.Rule, []*nftableslib.Rule, error) {
	ipv4 := make([]*nftableslib.Rule, 0)
	ipv6 := make([]*nftableslib.Rule, 0)

	if cidrs == "" {
		return ipv4, ipv6, nil
	}
	for _, cidr := range strings.Split(cidrs, ",") {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, nil, err
		}
		addr, _ := nftableslib.NewIPAddr(cidr)
		if ip.To4() != nil {
			ra, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_redirect_ipv4")
			// IPv4 CIDR, adding it to ipv4 slice of rules
			addr, _ := nftableslib.NewIPAddr(cidr)
			ipv4 = append(ipv4, &nftableslib.Rule{
				L3: &nftableslib.L3Rule{
					Dst: &nftableslib.IPAddrSpec{
						List: []*nftableslib.IPAddr{addr},
					},
				},
				Action: ra,
			})
			continue
		}
		// IPv6 CIDR, adding it to ipv4 slice of rules
		ra, _ := nftableslib.SetVerdict(unix.NFT_GOTO, "istio_redirect_ipv6")
		ipv6 = append(ipv6, &nftableslib.Rule{
			L3: &nftableslib.L3Rule{
				Dst: &nftableslib.IPAddrSpec{
					List: []*nftableslib.IPAddr{addr},
				},
			},
			Action: ra,
		})
	}

	return ipv4, ipv6, nil
}

func processExcludeCidrs(cidrs string) ([]*nftableslib.Rule, []*nftableslib.Rule, error) {
	ipv4 := make([]*nftableslib.Rule, 0)
	ipv6 := make([]*nftableslib.Rule, 0)

	if cidrs == "" {
		return ipv4, ipv6, nil
	}
	for _, cidr := range strings.Split(cidrs, ",") {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, nil, err
		}
		addr, _ := nftableslib.NewIPAddr(cidr)
		ra, _ := nftableslib.SetVerdict(unix.NFT_RETURN)
		if ip.To4() != nil {
			// IPv4 CIDR, adding it to ipv4 slice of rules

			ipv4 = append(ipv4, &nftableslib.Rule{
				L3: &nftableslib.L3Rule{
					Dst: &nftableslib.IPAddrSpec{
						List: []*nftableslib.IPAddr{addr},
					},
				},
				Action: ra,
			})
			continue
		}
		// IPv6 CIDR, adding it to ipv4 slice of rules
		ipv6 = append(ipv6, &nftableslib.Rule{
			L3: &nftableslib.L3Rule{
				Dst: &nftableslib.IPAddrSpec{
					List: []*nftableslib.IPAddr{addr},
				},
			},
			Action: ra,
		})
	}

	return ipv4, ipv6, nil
}

func generateOutboundExlcPorts(excludeOutboundPorts string) (*nftableslib.Rule, error) {
	list := make([]*uint16, 0)
	ports := strings.Split(excludeOutboundPorts, ",")
	for _, port := range ports {
		p, err := strconv.Atoi(port)
		if err != nil {
			return nil, err
		}
		if p > 65535 {
			return nil, fmt.Errorf("invalid port value of %d", p)
		}
		pp := uint16(p)
		list = append(list, &pp)
	}
	ra, _ := nftableslib.SetVerdict(unix.NFT_RETURN)
	return &nftableslib.Rule{
		L4: &nftableslib.L4Rule{
			L4Proto: unix.IPPROTO_TCP,
			Dst: &nftableslib.Port{
				List: list,
			},
		},
		Action: ra,
	}, nil
}
