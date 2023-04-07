package requestmeta

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/mirrorz-org/mirrorz-302/pkg/geo"
	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
)

type RequestMeta struct {
	CName string
	Tail  string

	Scheme string
	IP     net.IP
	Region string
	ISP    []string
	Labels []string
}

var parserLogger = logging.GetLogger("parser")

type Parser struct {
	DomainLength int
}

func (p *Parser) Parse(r *http.Request) (meta RequestMeta) {
	meta.CName, meta.Tail = p.CNameAndTail(r)
	meta.Scheme = p.Scheme(r)
	meta.IP = p.IP(r)
	ipinfo, err := geo.Lookup(meta.IP.String())
	if err != nil {
		parserLogger.Warningf("IPDB lookup failed for %s: %v\n", meta.IP, err)
	} else {
		meta.Region = geo.NameToCode(ipinfo.RegionName)
		for _, line := range strings.Split(ipinfo.Line, "/") {
			if isp := geo.ISPNameToCode(line); isp != "" {
				meta.ISP = append(meta.ISP, isp)
			}
		}
	}
	meta.Labels = p.Labels(r)
	return
}

func (m *RequestMeta) V4Only() bool {
	l := len(m.Labels)
	return l != 0 && m.Labels[l-1] == "4"
}

func (m *RequestMeta) V6Only() bool {
	l := len(m.Labels)
	return l != 0 && m.Labels[l-1] == "6"
}

func (m *RequestMeta) String() string {
	return fmt.Sprintf("%s:%s (%v, %s/%s) %v", m.Scheme, m.CName, m.IP, m.Region, m.ISP, m.Labels)
}

func (p *Parser) CNameAndTail(r *http.Request) (cname string, tail string) {
	// Remove leading '/'
	pathParts := strings.SplitN(r.URL.Path[1:], "/", 2)
	cname = pathParts[0]
	if len(pathParts) == 2 {
		tail = "/" + pathParts[1]
	}
	return
}

func (p *Parser) Scheme(r *http.Request) (scheme string) {
	scheme = r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	return
}

func (p *Parser) IP(r *http.Request) (ip net.IP) {
	ip = net.ParseIP(r.Header.Get("X-Real-IP"))
	return
}

func (p *Parser) Labels(r *http.Request) (labels []string) {
	dots := strings.Split(r.Header.Get("X-Forwarded-Host"), ".")
	if len(dots) != p.DomainLength {
		return
	}
	labels = strings.Split(dots[0], "-")
	return
}

func CacheKey(meta RequestMeta) string {
	return strings.Join([]string{
		meta.IP.String(),
		meta.CName,
		meta.Scheme,
		strings.Join(meta.Labels, "-"),
	}, "+")
}
