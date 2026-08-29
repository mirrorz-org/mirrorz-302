package mirrorzdb

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/stretchr/testify/assert"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%s): %v", s, err)
	}
	return n
}

// v4HTTPEndpoint builds a generic IPv4 HTTP-capable endpoint, ready for the
// IP/scheme used in the tests below (so earlier filter checks in Match pass).
func v4HTTPEndpoint() Endpoint {
	e := Endpoint{}
	e.Filter.V4 = true
	e.Filter.NOSSL = true
	return e
}

func TestMatchPrivateEndpoint(t *testing.T) {
	as := assert.New(t)

	meta := requestmeta.RequestMeta{
		IP:     net.ParseIP("202.0.0.5"),
		Scheme: "http",
	}

	// public:false and no CIDR -> disabled, never serves
	e := v4HTTPEndpoint()
	e.Public = false
	reason, ok := e.Match(meta)
	as.False(ok)
	as.Contains(reason, "disabled")

	// public:false with CIDR, IP in range -> OK
	e = v4HTTPEndpoint()
	e.Public = false
	e.RangeCIDR = []*net.IPNet{mustCIDR(t, "202.0.0.0/24")}
	reason, ok = e.Match(meta)
	as.True(ok)
	as.Equal("OK", reason)

	// public:false with CIDR, IP not in range -> rejected
	e = v4HTTPEndpoint()
	e.Public = false
	e.RangeCIDR = []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}
	reason, ok = e.Match(meta)
	as.False(ok)
	as.Contains(reason, "not in")

	// public:false with CIDR, IP not in range but ISP matches -> still rejected
	e = v4HTTPEndpoint()
	e.Public = false
	e.RangeCIDR = []*net.IPNet{mustCIDR(t, "10.0.0.0/24")}
	e.RangeISP = []string{"CERNET"}
	metaISP := meta
	metaISP.ISP = []string{"CERNET"}
	reason, ok = e.Match(metaISP)
	as.False(ok)
	as.Contains(reason, "not in")

	// public:false with only ISP/REGION (no CIDR) -> disabled
	e = v4HTTPEndpoint()
	e.Public = false
	e.RangeISP = []string{"CERNET"}
	e.RangeRegion = []string{"AH"}
	reason, ok = e.Match(metaISP)
	as.False(ok)
	as.Contains(reason, "disabled")

	// public:true and no CIDR -> OK (range only affects scoring)
	e = v4HTTPEndpoint()
	e.Public = true
	reason, ok = e.Match(meta)
	as.True(ok)
	as.Equal("OK", reason)
}

func TestMatchPrivateRangeRequiresKnownGeo(t *testing.T) {
	e := v4HTTPEndpoint()
	e.PrivateRanges = []PrivateRange{{Region: "ZJ", ISP: "CERNET"}}

	meta := requestmeta.RequestMeta{
		IP:       net.ParseIP("192.0.2.1"),
		Scheme:   "http",
		Region:   "ZJ",
		ISP:      []string{"CERNET"},
		GeoKnown: false,
	}
	reason, ok := e.Match(meta)
	assert.False(t, ok)
	assert.Contains(t, reason, "geolocation unavailable")

	meta.GeoKnown = true
	reason, ok = e.Match(meta)
	assert.True(t, ok, reason)

	meta.ISP = []string{"CHINANET"}
	reason, ok = e.Match(meta)
	assert.False(t, ok)
	assert.Contains(t, reason, "not in private range")
}

func TestPrivateRangeDoesNotRestrictCIDRMatch(t *testing.T) {
	e := v4HTTPEndpoint()
	e.RangeCIDR = []*net.IPNet{mustCIDR(t, "192.0.2.0/24")}
	e.PrivateRanges = []PrivateRange{{Region: "ZJ"}}

	reason, ok := e.Match(requestmeta.RequestMeta{
		IP:     net.ParseIP("192.0.2.1"),
		Scheme: "http",
	})
	assert.True(t, ok, reason)
}

func TestRejectInvalidPrivateRange(t *testing.T) {
	tests := map[string]string{
		"empty group":       `{"private_range":[[]]}`,
		"unknown condition": `{"private_range":[["IPS:CERNET"]]}`,
		"empty region":      `{"private_range":[["REGION:"]]}`,
		"empty isp":         `{"private_range":[["ISP:"]]}`,
		"duplicate region":  `{"private_range":[["REGION:ZJ","REGION:SH"]]}`,
		"duplicate isp":     `{"private_range":[["ISP:CERNET","ISP:CHINANET"]]}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			var endpoint Endpoint
			assert.Error(t, json.Unmarshal([]byte(data), &endpoint))
		})
	}
}
