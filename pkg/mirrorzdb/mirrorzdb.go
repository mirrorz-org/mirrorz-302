package mirrorzdb

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
)

var logger = logging.GetLogger("mirrorzdb")

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
	RangeRegion []string
	RangeISP    []string
	RangeCIDR   []*net.IPNet
}

// endpointJSON is used to parse Endpoint from JSON.
type endpointJSON struct {
	Label   string   `json:"label"`
	Resolve string   `json:"resolve"`
	Public  bool     `json:"public"`
	Filter  []string `json:"filter"`
	Range   []string `json:"range"`
}

// UnmarshalJSON implements the json.Unmarshaler interface.
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
		if region, ok := strings.CutPrefix(d, "REGION:"); ok {
			e.RangeRegion = append(e.RangeRegion, region)
		} else if isp, ok := strings.CutPrefix(d, "ISP:"); ok {
			e.RangeISP = append(e.RangeISP, isp)
		} else {
			_, ipnet, _ := net.ParseCIDR(d)
			if ipnet != nil {
				e.RangeCIDR = append(e.RangeCIDR, ipnet)
			}
		}
	}
	return nil
}

// Match checks if the endpoint can serve the request.
func (e *Endpoint) Match(m requestmeta.RequestMeta) (reason string, ok bool) {
	remoteIPv4 := m.IP.To4() != nil

	switch {
	case remoteIPv4 && !e.Filter.V4:
		return "not v4 endpoint", false
	case !remoteIPv4 && !e.Filter.V6:
		return "not v6 endpoint", false
	case m.Scheme == "http" && !e.Filter.NOSSL:
		return "not nossl endpoint", false
	case m.Scheme == "https" && !e.Filter.SSL:
		return "not ssl endpoint", false
	case m.V4Only() && !e.Filter.V4Only:
		return "label v4only but endpoint not v4only", false
	case m.V6Only() && !e.Filter.V6Only:
		return "label v6only but endpoint not v6only", false
	case !e.Public && !e.MatchISPs(m.ISP) && e.MatchIPMask(m.IP) == 0:
		return "private endpoint", false
	default:
		return "OK", true
	}
}

// MatchISP reports if the given ISP is preferred by the endpoint.
func (e *Endpoint) MatchISP(isp string) bool {
	for _, r := range e.RangeISP {
		if r == isp {
			return true
		}
	}
	return false
}

// MatchISPs reports if the given ISP set intersects with the endpoint's preference.
func (e *Endpoint) MatchISPs(isps []string) bool {
	for _, isp := range isps {
		if e.MatchISP(isp) {
			return true
		}
	}
	return false
}

// MatchIP reports if the given IP is preferred by the endpoint.
//
// Returns the longest matched CIDR.
func (e *Endpoint) MatchIPMask(ip net.IP) (longest int) {
	for _, ipnet := range e.RangeCIDR {
		if ipnet.Contains(ip) {
			mask, _ := ipnet.Mask.Size()
			if mask > longest {
				longest = mask
			}
		}
	}
	return
}

type Site struct {
	Abbr string `json:"abbr"`
}

type MirrorZDFile struct {
	Extension string     `json:"extension"`
	Endpoints []Endpoint `json:"endpoints"`
	Site      Site       `json:"site"`
}

type MirrorZDatabase struct {
	mu       sync.RWMutex
	files    []MirrorZDFile
	labelMap map[string]string
	abbrMap  map[string]*MirrorZDFile
}

func NewMirrorZDatabase() *MirrorZDatabase {
	return new(MirrorZDatabase)
}

func (m *MirrorZDatabase) Load(path string) (err error) {
	files, err := os.ReadDir(path)
	if err != nil {
		err = fmt.Errorf("MirrorZDatabase.Load: os.ReadDir: %w", err)
		logger.Errorf("%v\n", err)
		return
	}

	newFiles := make([]MirrorZDFile, 0, len(files))
	newLabelMap := make(map[string]string)
	newAbbrMap := make(map[string]*MirrorZDFile)

	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(path, file.Name()))
		if err != nil {
			logger.Errorf("LoadMirrorZD: read %s failed\n", file.Name())
			continue
		}
		var data MirrorZDFile
		if err := json.Unmarshal(content, &data); err != nil {
			logger.Errorf("LoadMirrorZD: Parse %s error: %v\n", file.Name(), err)
			continue
		}
		logger.Infof("%+v\n", data)
		idx := len(newFiles)
		newFiles = append(newFiles, data)
		newAbbrMap[data.Site.Abbr] = &newFiles[idx]

		for _, e := range data.Endpoints {
			newLabelMap[e.Label] = e.Resolve
		}
	}
	for label, resolve := range newLabelMap {
		logger.Infof("%s -> %s\n", label, resolve)
	}
	m.mu.Lock()
	m.files = newFiles
	m.labelMap = newLabelMap
	m.abbrMap = newAbbrMap
	m.mu.Unlock()
	return
}

// Files returns all files in the database.
//
// The returned slice must not be modified.
func (m *MirrorZDatabase) Files() []MirrorZDFile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.files
}

// Lookup returns the endpoints of the site.
func (m *MirrorZDatabase) Lookup(abbr string) (endpoints []Endpoint, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.abbrMap[abbr]; ok {
		return s.Endpoints, true
	}
	return
}

// Resolves a label to an endpoint URL.
func (m *MirrorZDatabase) ResolveLabel(label string) (resolve string, ok bool) {
	m.mu.RLock()
	resolve, ok = m.labelMap[label]
	m.mu.RUnlock()
	return
}
