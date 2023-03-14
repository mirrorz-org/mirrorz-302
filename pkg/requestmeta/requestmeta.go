package requestmeta

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
)

type RequestMeta struct {
	Scheme string
	IP     net.IP
	ASN    string
	Labels []string
}

type Parser struct {
	IPASNURL     string
	DomainLength int
	Logger       *logging.Logger
}

func (p *Parser) Parse(r *http.Request) (meta RequestMeta) {
	meta.Scheme = p.Scheme(r)
	meta.IP = p.IP(r)
	meta.ASN = p.ASN(meta.IP)
	meta.Labels = p.Host(r)
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

func (p *Parser) ASN(ip net.IP) (asn string) {
	client := http.Client{
		Timeout: 500 * time.Millisecond,
	}
	req := p.IPASNURL + "/" + ip.String()
	resp, err := client.Get(req)
	if err != nil {
		p.Logger.Errorf("IPASN HTTP Get failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.Logger.Errorf("IPASN read body failed: %v\n", err)
		return
	}
	asn = string(body)
	return
}

func (p *Parser) Host(r *http.Request) (labels []string) {
	dots := strings.Split(r.Header.Get("X-Forwarded-Host"), ".")
	if len(dots) != p.DomainLength {
		return
	}
	labels = strings.Split(dots[0], "-")
	return
}

func CacheKey(meta RequestMeta, cname string) string {
	return strings.Join([]string{
		meta.IP.String(),
		cname,
		meta.ASN,
		meta.Scheme,
		strings.Join(meta.Labels, "-"),
	}, "+")
}
