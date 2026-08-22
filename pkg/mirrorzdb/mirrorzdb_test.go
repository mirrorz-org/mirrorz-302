package mirrorzdb

import (
	"net"
	"os"
	"path/filepath"
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

func TestIsUnknown(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "extension": "D",
  "site": {"abbr": "TEST"},
  "mirrors": [
    {"cname": "unknown-repo", "url": "/unknown", "status": "U"},
    {"cname": "unknown-with-aux", "url": "/unknown-aux", "status": "UN123"},
    {"cname": "normal", "url": "/normal", "status": "S123"}
  ]
}`
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "test.json"), []byte(content), 0o600))
	db := NewMirrorZDatabase()
	assert.NoError(t, db.Load(dir))

	assert.True(t, db.IsUnknown("TEST", "unknown-repo"))
	assert.True(t, db.IsUnknown("TEST", "unknown-with-aux"))
	assert.False(t, db.IsUnknown("TEST", "normal"))
	assert.False(t, db.IsUnknown("OTHER", "unknown-repo"))
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
