package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Endpoint struct {
	Label   string   `json:"label"`
	Resolve string   `json:"resolve"`
	Public  bool     `json:"public"`
	Filter  []string `json:"filter"`
	Range   []string `json:"range"`
}

type EndpointInternal struct {
	Label   string
	Resolve string
	Public  bool
	Filter  struct {
		V4      bool
		V4Only  bool
		V6      bool
		V6Only  bool
		SSL     bool
		NOSSL   bool
		SPECIAL []string
	}
	RangeASN  []string
	RangeCIDR []*net.IPNet
}

type Site struct {
	Abbr string `json:"abbr"`
}

type MirrorZD struct {
	Extension string     `json:"extension"`
	Endpoints []Endpoint `json:"endpoints"`
	Site      Site       `json:"site"`
}

// map[string]string
var LabelToResolve sync.Map

// map[string][]EndpointInternal
var AbbrToEndpoints sync.Map

func ProcessEndpoint(e Endpoint) (i EndpointInternal) {
	Label := strings.ReplaceAll(e.Label, "-", "")
	LabelToResolve.Store(Label, e.Resolve)
	i.Label = Label
	i.Resolve = e.Resolve
	i.Public = e.Public
	// Filter
	for _, d := range e.Filter {
		if d == "V4" {
			i.Filter.V4 = true
		} else if d == "V6" {
			i.Filter.V6 = true
		} else if d == "NOSSL" {
			i.Filter.NOSSL = true
		} else if d == "SSL" {
			i.Filter.SSL = true
		} else {
			// TODO: more structured
			i.Filter.SPECIAL = append(i.Filter.SPECIAL, d)
		}
	}
	if i.Filter.V4 && !i.Filter.V6 {
		i.Filter.V4Only = true
	}
	if !i.Filter.V4 && i.Filter.V6 {
		i.Filter.V6Only = true
	}
	// Range
	for _, d := range e.Range {
		if strings.HasPrefix(d, "AS") {
			i.RangeASN = append(i.RangeASN, d[2:])
		} else {
			_, ipnet, _ := net.ParseCIDR(d)
			if ipnet != nil {
				i.RangeCIDR = append(i.RangeCIDR, ipnet)
			}
		}
	}
	return
}

func LoadMirrorZD(path string) (err error) {
	files, err := os.ReadDir(path)
	if err != nil {
		logger.Errorf("LoadMirrorZD: can not open mirrorz.d directory, %v\n", err)
		return
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(path, file.Name()))
		if err != nil {
			logger.Errorf("LoadMirrorZD: read %s failed\n", file.Name())
			continue
		}
		var data MirrorZD
		err = json.Unmarshal(content, &data)
		if err != nil {
			logger.Errorf("LoadMirrorZD: process %s failed\n", file.Name())
			continue
		}
		logger.Infof("%+v\n", data)
		var endpointsInternal []EndpointInternal
		for _, e := range data.Endpoints {
			endpointsInternal = append(endpointsInternal, ProcessEndpoint(e))
		}
		AbbrToEndpoints.Store(data.Site.Abbr, endpointsInternal)
	}
	LabelToResolve.Range(func(label interface{}, resolve interface{}) bool {
		logger.Infof("%s -> %s\n", label, resolve)
		return true
	})
	return
}
