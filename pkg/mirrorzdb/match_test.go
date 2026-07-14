package mirrorzdb

import (
	"net"
	"testing"

	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/stretchr/testify/assert"
)

func makeEndpoint(label, siteLabel string) Endpoint {
	e := Endpoint{
		Label:     label,
		Resolve:   "example.com",
		Public:    true,
		SiteLabel: siteLabel,
	}
	e.Filter.V4 = true
	e.Filter.V6 = true
	e.Filter.SSL = true
	e.Filter.NOSSL = true
	return e
}

func baseMeta(labels []string) requestmeta.RequestMeta {
	return requestmeta.RequestMeta{
		IP:     net.ParseIP("1.2.3.4"),
		Scheme: "https",
		Labels: labels,
	}
}

func TestMatchAvoidSite(t *testing.T) {
	as := assert.New(t)
	endpoints := []Endpoint{
		makeEndpoint("ustc", "ustc"),
		makeEndpoint("ustc6", "ustc"),
		makeEndpoint("ustccampus", "ustc"),
	}
	meta := baseMeta([]string{"avoidustc"})
	for _, e := range endpoints {
		reason, ok := e.Match(meta)
		as.False(ok, "endpoint %s should be avoided, got %s", e.Label, reason)
		as.Equal("avoid site", reason)
	}
}

func TestMatchAvoidEndpoint(t *testing.T) {
	as := assert.New(t)
	rep := makeEndpoint("ustc", "ustc")
	specific := makeEndpoint("ustc6", "ustc")
	other := makeEndpoint("ustccampus", "ustc")

	meta := baseMeta([]string{"avoidustc6"})
	_, ok := rep.Match(meta)
	as.True(ok, "representative endpoint must not be affected by avoid of another endpoint")
	_, ok = other.Match(meta)
	as.True(ok, "unrelated endpoint must not be affected")
	reason, ok := specific.Match(meta)
	as.False(ok, "ustc6 should be avoided")
	as.Equal("avoid endpoint", reason)
}

func TestMatchNoAvoid(t *testing.T) {
	as := assert.New(t)
	e := makeEndpoint("ustc", "ustc")
	meta := baseMeta([]string{"tuna", "ustc"})
	reason, ok := e.Match(meta)
	as.True(ok, reason)
}

func TestMatchPreferAndAvoidConflict(t *testing.T) {
	as := assert.New(t)
	e := makeEndpoint("ustc", "ustc")
	// both prefer and avoid the same label; avoid wins (excluded)
	meta := baseMeta([]string{"ustc", "avoidustc"})
	reason, ok := e.Match(meta)
	as.False(ok, "avoid should take precedence over prefer")
	as.Equal("avoid site", reason)
}

func TestMatchEmptySiteLabel(t *testing.T) {
	as := assert.New(t)
	// SiteLabel empty (e.g. not set); only endpoint-level avoid applies
	e := makeEndpoint("ustc", "")
	e.Filter.V4 = true
	e.Filter.V6 = true
	e.Filter.SSL = true
	e.Filter.NOSSL = true
	meta := baseMeta([]string{"avoidustc"})
	reason, ok := e.Match(meta)
	as.False(ok)
	as.Equal("avoid endpoint", reason)
}
