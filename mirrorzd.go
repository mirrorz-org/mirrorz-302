package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
)

var logger = loggo.GetLogger("mirrorzd")

type Endpoint struct {
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
		Special []string
	}
	RangeASN  []string
	RangeCIDR []*net.IPNet
}

// endpointJSON is used to parse Endpoint from JSON.
type endpointJSON struct {
	Label   string   `json:"label"`
	Resolve string   `json:"resolve"`
	Public  bool     `json:"public"`
	Filter  []string `json:"filter"`
	Range   []string `json:"range"`
}

func (e *Endpoint) UnmarshalJSON(data []byte) error {
	var j endpointJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}

	label := strings.ReplaceAll(j.Label, "-", "")
	e.Label = label
	e.Resolve = j.Resolve
	e.Public = j.Public
	// Filter
	for _, d := range j.Filter {
		switch d {
		case "V4":
			e.Filter.V4 = true
		case "V6":
			e.Filter.V6 = true
		case "NOSSL":
			e.Filter.NOSSL = true
		case "SSL":
			e.Filter.SSL = true
		default:
			// TODO: more structured
			e.Filter.Special = append(e.Filter.Special, d)
		}
	}
	if e.Filter.V4 && !e.Filter.V6 {
		e.Filter.V4Only = true
	}
	if !e.Filter.V4 && e.Filter.V6 {
		e.Filter.V6Only = true
	}
	// Range
	for _, d := range j.Range {
		if strings.HasPrefix(d, "AS") {
			e.RangeASN = append(e.RangeASN, d[2:])
		} else {
			_, ipnet, _ := net.ParseCIDR(d)
			if ipnet != nil {
				e.RangeCIDR = append(e.RangeCIDR, ipnet)
			}
		}
	}
	return nil
}

func (e *Endpoint) Match(m requestmeta.RequestMeta) (reason string, ok bool) {
	remoteIPv4 := m.IP.To4() != nil

	if remoteIPv4 && !e.Filter.V4 {
		return "not v4 endpoint", false
	} else if !remoteIPv4 && !e.Filter.V6 {
		return "not v6 endpoint", false
	} else if m.Scheme == "http" && !e.Filter.NOSSL {
		return "not nossl endpoint", false
	}
	if m.Scheme == "https" && !e.Filter.SSL {
		return "not ssl endpoint", false
	}
	if m.V4Only() && !e.Filter.V4Only {
		return "label v4only but endpoint not v4only", false
	}
	if m.V6Only() && !e.Filter.V6Only {
		return "label v6only but endpoint not v6only", false
	}
	return "OK", true
}

func (e *Endpoint) Score(m requestmeta.RequestMeta) (score Score) {
	for index, label := range m.Labels {
		if label == e.Label {
			score.pos = index + 1
			break
		}
	}
	for _, endpointASN := range e.RangeASN {
		if endpointASN == m.ASN {
			score.as = 1
			break
		}
	}
	for _, ipnet := range e.RangeCIDR {
		if m.IP != nil && ipnet.Contains(m.IP) {
			mask, _ := ipnet.Mask.Size()
			if mask > score.mask {
				score.mask = mask
			}
		}
	}
	score.resolve = e.Resolve
	return
}

type Site struct {
	Abbr string `json:"abbr"`
}

type MirrorZDData struct {
	Extension string     `json:"extension"`
	Endpoints []Endpoint `json:"endpoints"`
	Site      Site       `json:"site"`
}

type MirrorZD struct {
	// map[string]string
	labelToResolve sync.Map

	// map[string][]Endpoint
	abbrToEndpoints sync.Map
}

func NewMirrorZD() *MirrorZD {
	return new(MirrorZD)
}

func (m *MirrorZD) Load(path string) (err error) {
	files, err := os.ReadDir(path)
	if err != nil {
		logger.Errorf("LoadMirrorZD: cannot open mirrorz.d directory: %v\n", err)
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
		var data MirrorZDData
		if err := json.Unmarshal(content, &data); err != nil {
			logger.Errorf("LoadMirrorZD: Parse %s error: %v\n", file.Name(), err)
			continue
		}
		logger.Infof("%+v\n", data)

		for _, e := range data.Endpoints {
			m.labelToResolve.Store(e.Label, e.Resolve)
		}
		m.abbrToEndpoints.Store(data.Site.Abbr, data.Endpoints)
	}
	m.labelToResolve.Range(func(label interface{}, resolve interface{}) bool {
		logger.Infof("%s -> %s\n", label, resolve)
		return true
	})
	return
}

func (m *MirrorZD) Lookup(abbr string) (endpoints []Endpoint, ok bool) {
	ep, ok := m.abbrToEndpoints.Load(abbr)
	if !ok {
		return
	}
	endpoints, ok = ep.([]Endpoint)
	return
}

func (m *MirrorZD) ResolveLabel(label string) (resolve string, ok bool) {
	r, ok := m.labelToResolve.Load(label)
	if !ok {
		return
	}
	resolve, ok = r.(string)
	return
}
