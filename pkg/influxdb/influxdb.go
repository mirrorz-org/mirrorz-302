package influxdb

import (
	"context"
	"fmt"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
)

type Config struct {
	URL    string `json:"url"`
	Token  string `json:"token"`
	Org    string `json:"org"`
	Bucket string `json:"bucket"`
}

type Source struct {
	bucket string

	client   influxdb2.Client
	queryAPI api.QueryAPI
}

func NewSource(url, token, org, bucket string) *Source {
	client := influxdb2.NewClient(url, token)
	queryAPI := client.QueryAPI(org)
	return &Source{
		bucket:   bucket,
		client:   client,
		queryAPI: queryAPI,
	}
}

func NewSourceFromConfig(config Config) *Source {
	return NewSource(config.URL, config.Token, config.Org, config.Bucket)
}

// func OpenInfluxDB(url, token, org, bucket string) {
// 	client = influxdb2.NewClient(config.InfluxDBURL, config.InfluxDBToken)
// 	queryAPI = client.QueryAPI(config.InfluxDBOrg)
// }

func (s *Source) Close() {
	s.client.Close()
}

type Result = *api.QueryTableResult

func (s *Source) Query(ctx context.Context, cname string) (Result, error) {
	query := fmt.Sprintf(`from(bucket: "%s")
        |> range(start: -15m)
        |> filter(fn: (r) => r._measurement == "repo" and r.name == "%s")
        |> map(fn: (r) => ({_value: r._value, mirror: r.mirror, _time: r._time,path: r.url}))
        |> tail(n: 1)`, s.bucket, cname)
	// SQL INJECTION!!! (use read only token)
	return s.queryAPI.Query(ctx, query)
}
