package main

import (
	"bytes"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/osrg/gobgp/config"
	"io/ioutil"
	"net"
	"os"
)

func main() {
	b := config.Bgp{
		Global: config.Global{
			As:       64512,
			RouterId: net.ParseIP("192.168.10.155"),
		},
		NeighborList: []config.Neighbor{
			config.Neighbor{
				PeerAs:          64512,
				NeighborAddress: net.ParseIP("172.17.0.7"),
				// AuthPassword:    "apple",
				AfiSafiList: []config.AfiSafi{
					// config.AfiSafi{
					// 	AfiSafiName: "ipv4-unicast",
					// },
					config.AfiSafi{
						AfiSafiName: "l2vpn-evpn",
						L2vpnEvpn:   config.L2vpnEvpn{},
					},
				},
				Timers: config.Timers{
					ConnectRetry:      100,
					HoldTime:          180,
					KeepaliveInterval: 10,
				},
			},
			config.Neighbor{
				PeerAs:          64512,
				NeighborAddress: net.ParseIP("172.17.0.5"),
				AfiSafiList: []config.AfiSafi{
					config.AfiSafi{
						AfiSafiName: "ipv4-unicast",
					},
					config.AfiSafi{
						AfiSafiName: "l2vpn-evpn",
						L2vpnEvpn:   config.L2vpnEvpn{},
					},
				},
				Timers: config.Timers{
					ConnectRetry:      100,
					HoldTime:          180,
					KeepaliveInterval: 10,
				},
			},
			// config.Neighbor{
			// 	PeerAs:          12335,
			// 	NeighborAddress: net.ParseIP("192.168.177.34"),
			// 	AuthPassword:    "grape",
			// },
		},
	}

	var buffer bytes.Buffer
	encoder := toml.NewEncoder(&buffer)
	err := encoder.Encode(b)
	if err != nil {
		panic(err)
	}

	content := []byte(buffer.String())
	ioutil.WriteFile("gobgpd.conf", content, os.ModePerm)

	fmt.Printf("%v\n", buffer.String())
}
