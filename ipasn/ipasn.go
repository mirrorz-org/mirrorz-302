package main

import (
	"bufio"
	"os"
	"strings"

	"fmt"
	"net"
	"net/http"

	"github.com/juju/loggo"

	"encoding/json"

	"flag"
)

var logger = loggo.GetLogger("ipasn")

type Provider struct {
	root4 CIDRTrieNode
	root6 CIDRTrieNode
}

type CIDRTrieNode struct {
	value string
	zero  *CIDRTrieNode
	one   *CIDRTrieNode
}

func GetBit(ip net.IP, index int) int {
	i := index / 8
	j := index % 8
	return int((ip[i] & (1 << (7 - j))) >> (7 - j))
}

func (p *Provider) Put(cidr string, value string) {
	ipaddr, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		logger.Errorf("CIDR parse error: %s\n", cidr)
		return
	}
	ipmask, _ := ipnet.Mask.Size()
	//logger.Debugf("Put cidr: %s value: %s\n", cidr, value)

	var root *CIDRTrieNode
	if ipaddr.To4() != nil { // IPv4
		root = &p.root4
		ipaddr = ipaddr.To4()
	} else if ipaddr.To16() != nil { // IPv6
		root = &p.root6
		ipaddr = ipaddr.To16()
	}
	for depth := 0; depth < ipmask; depth++ {
		if GetBit(ipaddr, depth) == 1 {
			if root.one == nil {
				root.one = new(CIDRTrieNode)
			}
			root = root.one
		} else {
			if root.zero == nil {
				root.zero = new(CIDRTrieNode)
			}
			root = root.zero
		}
	}
	root.value = value
}

func (p *Provider) Get(ip string) (value string) {
	ipaddr := net.ParseIP(ip)
	logger.Debugf("IP: %s\n", ip)
	if ipaddr == nil {
		logger.Errorf("IP parse error: %s\n", ip)
		return
	}

	var root *CIDRTrieNode
	if ipaddr.To4() != nil { // IPv4
		root = &p.root4
		ipaddr = ipaddr.To4()
	} else if ipaddr.To16() != nil { // IPv6
		root = &p.root6
		ipaddr = ipaddr.To16()
	}
	logger.Debugf("IPLen: %d\n", len(ipaddr))

	for depth := 0; depth < len(ipaddr)*8; depth++ {
		bit := GetBit(ipaddr, depth)
		if bit == 1 {
			root = root.one
		} else {
			root = root.zero
		}
		if root == nil {
			break
		}
		if root.value != "" {
			value = root.value
		}
		logger.Debugf("depth: %d value: %s bit: %d\n", depth, root.value, bit)
	}
	logger.Debugf("IP: %s %s\n", ip, value)
	return
}

func (p *Provider) Load(filename string) {
	file, err := os.Open(filename)
	if err != nil {
		logger.Errorf("%v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		array := strings.Fields(scanner.Text())
		p.Put(array[0], array[1])
	}
	logger.Debugf("DB4: %t %t\n", p.root4.one == nil, p.root4.zero == nil)
	logger.Debugf("DB6: %t %t\n", p.root6.one == nil, p.root6.zero == nil)
}

func (p *Provider) Reset() {
	p.root4 = CIDRTrieNode{}
	p.root6 = CIDRTrieNode{}
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	arg := r.URL.Path[1:]
	fmt.Fprintf(w, "%s", p.Get(arg))
}

type Config struct {
	HTTPBindAddress string `json:"http-bind-address"`
	ASNDatabase     string `json:"asn-db"`
}

var config Config

func LoadConfig(path string, debug bool) (err error) {
	if debug {
		loggo.ConfigureLoggers("ipasn=DEBUG")
	} else {
		loggo.ConfigureLoggers("ipasn=INFO")
	}

	file, err := os.ReadFile(path)
	if err != nil {
		logger.Errorf("LoadConfig ReadFile failed: %v\n", err)
		return
	}
	err = json.Unmarshal([]byte(file), &config)
	if err != nil {
		logger.Errorf("LoadConfig json Unmarshal failed: %v\n", err)
		return
	}
	if config.ASNDatabase == "" {
		logger.Errorf("LoadConfig find no ASNDatabase in file\n")
		return
	}
	if config.HTTPBindAddress == "" {
		config.HTTPBindAddress = "localhost:8889"
	}
	logger.Debugf("LoadConfig ASN Database: %s\n", config.ASNDatabase)
	logger.Debugf("LoadConfig HTTP Bind Address: %s\n", config.HTTPBindAddress)
	return
}

func main() {
	configPtr := flag.String("config", "config.json", "path to config file")
	debugPtr := flag.Bool("debug", false, "debug mode")
	flag.Parse()
	LoadConfig(*configPtr, *debugPtr)

	p := new(Provider)
	p.Load(config.ASNDatabase)

	logger.Infof("Finish reading ASN database")

	http.Handle("/", p)
	logger.Errorf("HTTP Server error: %v\n", http.ListenAndServe(config.HTTPBindAddress, nil))
}
