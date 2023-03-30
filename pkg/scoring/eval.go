package scoring

import (
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
)

// Eval calculates the score for the endpoint with a given request.
func Eval(e *mirrorzdb.Endpoint, m requestmeta.RequestMeta) (score Score) {
	for index, label := range m.Labels {
		if label == e.Label {
			score.Pos = index + 1
			// Note: The last label takes precedence, so don't `break` here.
		}
	}
	for _, endpointASN := range e.RangeASN {
		if endpointASN == m.ASN {
			score.AS = 1
			break
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
	score.Resolve = e.Resolve
	return
}
