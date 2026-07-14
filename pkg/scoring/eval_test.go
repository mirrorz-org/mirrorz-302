package scoring

import (
	"testing"

	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/stretchr/testify/assert"
)

func TestEvalPrefer(t *testing.T) {
	as := assert.New(t)
	e := mirrorzdb.Endpoint{Label: "ustc"}
	meta := requestmeta.RequestMeta{Labels: []string{"tuna", "ustc", "4"}}
	score := Eval(e, meta)
	as.Equal(2, score.Pos)
}

func TestEvalPreferLastWins(t *testing.T) {
	as := assert.New(t)
	e := mirrorzdb.Endpoint{Label: "ustc"}
	meta := requestmeta.RequestMeta{Labels: []string{"ustc", "tuna", "ustc"}}
	score := Eval(e, meta)
	as.Equal(3, score.Pos)
}

func TestEvalAvoidNoLongerAffectsPos(t *testing.T) {
	as := assert.New(t)
	e := mirrorzdb.Endpoint{Label: "ustc"}
	meta := requestmeta.RequestMeta{Labels: []string{"avoidustc"}}
	score := Eval(e, meta)
	// avoid is now handled by Match (exclusion); Eval no longer sets Pos=-1
	as.Equal(0, score.Pos)
}

func TestEvalNoLabel(t *testing.T) {
	as := assert.New(t)
	e := mirrorzdb.Endpoint{Label: "ustc"}
	meta := requestmeta.RequestMeta{Labels: []string{"tuna", "bjtu"}}
	score := Eval(e, meta)
	as.Equal(0, score.Pos)
}
