package server

import (
	"testing"

	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/stretchr/testify/assert"
)

func TestCalcDeltaCutoff(t *testing.T) {
	as := assert.New(t)
	data := []int{-11, -1, -1, -1, -1, -1, -1, -1, -1, -1,
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	payload := make(influxdb.Result, len(data))
	for i, d := range data {
		payload[i].Value = d
	}
	// avg = -2, std = 3, zero and positive values are ignored
	as.Equal(-8, calcDeltaCutoff(payload))
}
