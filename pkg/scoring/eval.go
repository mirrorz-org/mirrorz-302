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

ISPOuterLoop:
	for _, isp := range e.RangeISP {
		for _, mISP := range m.ISP {
			if isp == mISP {
				score.ISP = 1
				break ISPOuterLoop
			}
		}
	}

	for _, ipnet := range e.RangeCIDR {
		if m.IP != nil && ipnet.Contains(m.IP) {
			mask, _ := ipnet.Mask.Size()
			if mask > score.Mask {
				score.Mask = mask
			}
		}
	}
	score.Label = e.Label
	score.Resolve = e.Resolve
	return
}
