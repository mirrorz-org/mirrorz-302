package main

import (
	"context"
	"fmt"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
)

var (
	client   influxdb2.Client
	queryAPI api.QueryAPI
)

func OpenInfluxDB() {
	client = influxdb2.NewClient(config.InfluxDBURL, config.InfluxDBToken)
	queryAPI = client.QueryAPI(config.InfluxDBOrg)
}

func CloseInfluxDB() {
	client.Close()
}

func QueryInflux(ctx context.Context, cname string) (*api.QueryTableResult, error) {
	query := fmt.Sprintf(`from(bucket: "%s")
        |> range(start: -15m)
        |> filter(fn: (r) => r._measurement == "repo" and r.name == "%s")
        |> map(fn: (r) => ({_value: r._value, mirror: r.mirror, _time: r._time,path: r.url}))
        |> tail(n: 1)`, config.InfluxDBBucket, cname)
	// SQL INJECTION!!! (use read only token)
	return queryAPI.Query(ctx, query)
}
