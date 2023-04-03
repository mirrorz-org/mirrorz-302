package geo

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

type geoDistanceData struct {
	Code1, Code2 string
	Ref          float64
}

var geoDistanceDataList = []geoDistanceData{
	{"BJ", "SH", 1066e3}, // Beijing - Shanghai
	{"BJ", "HK", 1966e3}, // Beijing - Hong Kong
	{"SH", "SN", 1219e3}, // Shanghai - Xi'an
	{"HB", "XZ", 2227e3}, // Wuhan - Lhasa
	{"GS", "XJ", 1624e3}, // Lanzhou - Urumqi
}

const geoDistanceTolerance = 5e3 // allow 5km error

func TestGeoDistance(t *testing.T) {
	as := assert.New(t)
	for _, data := range geoDistanceDataList {
		code1, code2, ref := data.Code1, data.Code2, data.Ref
		result := GeoDistance(code1, code2)
		as.Lessf(math.Abs(result-ref), geoDistanceTolerance,
			"Distance from %s to %s should be %.f km, but got %.f km", code1, code2, ref/1e3, result/1e3)
	}
}
