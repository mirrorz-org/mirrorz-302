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
		V4           bool
		V4Only       bool
		V6           bool
		V6Only       bool
		SSL          bool
		NOSSL        bool
		Special      []string
		Region       []string
		RegionOption bool
		ISP          []string
		ISPOption    bool
	}
	RangeOption       bool
	RangeRegion       []string
	RangeRegionOption bool
	RangeISP          []string
	RangeISPOption    bool
	RangeCIDR         []*net.IPNet
	// SiteLabel is the representative label of the site this endpoint
	// belongs to (the first endpoint's label), set during Load. It is
	// used so that `avoid<SiteLabel>` excludes the whole site.
	SiteLabel string
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
			if region, ok := strings.CutPrefix(d, "REGION:"); ok {
				e.Filter.Region = append(e.Filter.Region, region)
			} else if isp, ok := strings.CutPrefix(d, "ISP:"); ok {
				e.Filter.ISP = append(e.Filter.ISP, isp)
			} else {
				// TODO: more structured
				e.Filter.Special = append(e.Filter.Special, d)
			}

		}
	}
	e.Filter.ISPOption = e.Filter.ISP != nil
	e.Filter.RegionOption = e.Filter.Region != nil
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
	e.RangeISPOption = e.RangeISP != nil
	e.RangeRegionOption = e.RangeRegion != nil
	return nil
}

// Match checks if the endpoint can serve the request.
func (e *Endpoint) Match(m requestmeta.RequestMeta) (reason string, ok bool) {
	for _, l := range m.Labels {
		if e.SiteLabel != "" && l == "avoid"+e.SiteLabel {
			return "avoid site", false
		}
		if l == "avoid"+e.Label {
			return "avoid endpoint", false
		}
	}

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
	// Special filters for private site
	case !e.Public && e.MatchIPMask(m.IP) == 0:
		// Request must be included in filtered region AND ISP
		if e.Filter.ISPOption || e.Filter.RegionOption {
			if (e.Filter.RegionOption && !MatchRegion(m.Region, e.Filter.Region)) ||
				(e.Filter.ISPOption && !MatchISPs(m.ISP, e.Filter.ISP)) {
				return "ip not in filtered range", false
			}
			// Request should be included in filtered region OR ISP
		} else if e.RangeISPOption || e.RangeRegionOption {
			if (!e.RangeRegionOption || !MatchRegion(m.Region, e.RangeRegion)) &&
				(!e.RangeISPOption || !MatchISPs(m.ISP, e.RangeISP)) {
				return "ip not in private range", false
			}
		} else {
			// This site may not define any region or isp rules
			return "ip not in cidr range", false
		}
		return "OK", true
	default:
		return "OK", true
	}
}

// MatchRegion reports if the given region is in the config of the endpoint
func MatchRegion(region string, regions []string) bool {
	for _, r := range regions {
		if r == region {
			return true
		}
	}
	return false
}

// MatchISP reports if the given ISP is in the config of the endpoint.
func MatchISP(isp string, isps []string) bool {
	for _, r := range isps {
		if r == isp {
			return true
		}
	}
	return false
}

// MatchISPs reports if the given ISP set intersects with the endpoint's preference.
func MatchISPs(isps1 []string, isps2 []string) bool {
	for _, isp := range isps1 {
		if MatchISP(isp, isps2) {
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

type SiteFile struct {
	Abbrs     []string   `json:"abbrs"`
	Endpoints []Endpoint `json:"endpoints"`
}

type MirrorZDatabase struct {
	mu       sync.RWMutex
	abbrs    []string
	labelMap map[string]string
	abbrMap  map[string][]Endpoint
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

	newAbbrs := make([]string, 0, len(files))
	newLabelMap := make(map[string]string)
	newAbbrMap := make(map[string][]Endpoint)

	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(path, file.Name()))
		if err != nil {
			return fmt.Errorf("MirrorZDatabase.Load: read %s: %w", file.Name(), err)
		}
		var data SiteFile
		if err := json.Unmarshal(content, &data); err != nil {
			return fmt.Errorf("MirrorZDatabase.Load: parse %s: %w", file.Name(), err)
		}
		if len(data.Abbrs) == 0 {
			return fmt.Errorf("MirrorZDatabase.Load: %s has no abbrs", file.Name())
		}
		if len(data.Endpoints) == 0 {
			return fmt.Errorf("MirrorZDatabase.Load: %s has no endpoints", file.Name())
		}

		siteLabel := data.Endpoints[0].Label
		for i := range data.Endpoints {
			data.Endpoints[i].SiteLabel = siteLabel
		}
		for _, abbr := range data.Abbrs {
			if abbr == "" {
				return fmt.Errorf("MirrorZDatabase.Load: %s has an empty abbr", file.Name())
			}
			if _, exists := newAbbrMap[abbr]; exists {
				return fmt.Errorf("MirrorZDatabase.Load: duplicate abbr %q", abbr)
			}
			newAbbrs = append(newAbbrs, abbr)
			newAbbrMap[abbr] = data.Endpoints
		}

		for _, e := range data.Endpoints {
			newLabelMap[e.Label] = e.Resolve
		}
	}
	for label, resolve := range newLabelMap {
		logger.Infof("%s -> %s\n", label, resolve)
	}
	m.mu.Lock()
	m.abbrs = newAbbrs
	m.labelMap = newLabelMap
	m.abbrMap = newAbbrMap
	m.mu.Unlock()
	return
}

// Abbrs returns all mirror abbreviations in the database.
//
// The returned slice must not be modified.
func (m *MirrorZDatabase) Abbrs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abbrs
}

// Lookup returns the endpoints of the site.
func (m *MirrorZDatabase) Lookup(abbr string) (endpoints []Endpoint, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	endpoints, ok = m.abbrMap[abbr]
	return
}

// Resolves a label to an endpoint URL.
func (m *MirrorZDatabase) ResolveLabel(label string) (resolve string, ok bool) {
	m.mu.RLock()
	resolve, ok = m.labelMap[label]
	m.mu.RUnlock()
	return
}
