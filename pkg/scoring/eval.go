package scoring

import (
	"math"

	"github.com/mirrorz-org/mirrorz-302/pkg/geo"
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
)

// Eval calculates the score for the endpoint with a given request.
func Eval(e mirrorzdb.Endpoint, m requestmeta.RequestMeta) (score Score) {
	for index, label := range m.Labels {
		if label == e.Label {
			score.Pos = index + 1
			// Note: The last label takes precedence, so don't `break` here.
		}
	}

	score.Geo = math.Inf(1)
	for _, region := range e.RangeRegion {
		d := geo.GeoDistance(m.Region, region)
		if d < score.Geo {
			score.Geo = d
		}
	}

	for _, isp := range m.ISP {
		if e.MatchISP(isp) {
			score.ISP = 1
		}
	}

	if m.IP != nil {
		score.Mask = e.MatchIPMask(m.IP)
	}
	score.Label = e.Label
	score.Resolve = e.Resolve
	return
}
