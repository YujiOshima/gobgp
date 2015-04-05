package main

import (
	"bytes"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/jessevdk/go-flags"
	"github.com/osrg/gobgp/config"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
)

type BagpipebgpConfig struct {
	id          int
	config      *config.Neighbor
	gobgpConfig *config.Global
	serverIP    net.IP
}

func NewBagpipebgpConfig(id int, gConfig *config.Global, myConfig *config.Neighbor, server net.IP) *BagpipebgpConfig {
	return &BagpipebgpConfig{
		id:          id,
		config:      myConfig,
		gobgpConfig: gConfig,
		serverIP:    server,
	}
}

func (qt *BagpipebgpConfig) Config() *bytes.Buffer {
	buf := bytes.NewBuffer(nil)

	buf.WriteString("[BGP]\n")
	buf.WriteString(fmt.Sprintf("local_address=192.168.0.%d\n", qt.id))
	buf.WriteString(fmt.Sprintf("peers=%s\n", qt.serverIP, qt.gobgpConfig.As))
	buf.WriteString(fmt.Sprintf("my_as=%d\n", qt.gobgpConfig.As))
	buf.WriteString("[API]\n")
	buf.WriteString("api_host=localhost \n")
	buf.WriteString("api_port=8082\n")
	buf.WriteString("[DATAPLANE_DRIVER_EVPN]\n")
	buf.WriteString("dataplane_driver = DummyDataplaneDriver\n")
	return buf
}

func create_config_files(nr int, outputDir string) {
	bagpipebgpConfigList := make([]*BagpipebgpConfig, 0)

	gobgpConf := config.Bgp{
		Global: config.Global{
			As:       65000,
			RouterId: net.ParseIP("192.168.255.1"),
		},
	}

	for i := 1; i < nr+1; i++ {
		c := config.Neighbor{
			PeerAs:          65000 + uint32(i),
			NeighborAddress: net.ParseIP(fmt.Sprintf("10.0.0.%d", i)),
			AuthPassword:    fmt.Sprintf("hoge%d", i),
		}
		gobgpConf.NeighborList = append(gobgpConf.NeighborList, c)
		q := NewBagpipebgpConfig(i, &gobgpConf.Global, &c, net.ParseIP("10.0.255.1"))
		bagpipebgpConfigList = append(bagpipebgpConfigList, q)
		os.Mkdir(fmt.Sprintf("%s/q%d", outputDir, i), 0755)
		err := ioutil.WriteFile(fmt.Sprintf("%s/q%d/bgp.conf", outputDir, i), q.Config().Bytes(), 0644)
		if err != nil {
			log.Fatal(err)
		}
	}

	var buffer bytes.Buffer
	encoder := toml.NewEncoder(&buffer)
	encoder.Encode(gobgpConf)

	err := ioutil.WriteFile(fmt.Sprintf("%s/gobgpd.conf", outputDir), buffer.Bytes(), 0644)
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	var opts struct {
		ClientNumber int    `short:"n" long:"client-number" description:"specfying the number of clients" default:"3"`
		OutputDir    string `short:"c" long:"output" description:"specifing the output directory"`
	}
	parser := flags.NewParser(&opts, flags.Default)

	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}

	if opts.OutputDir == "" {
		opts.OutputDir, _ = filepath.Abs(".")
	} else {
		if _, err := os.Stat(opts.OutputDir); os.IsNotExist(err) {
			os.Mkdir(opts.OutputDir, 0755)
		}
	}

	create_config_files(opts.ClientNumber, opts.OutputDir)
}
