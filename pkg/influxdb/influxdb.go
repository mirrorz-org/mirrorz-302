package influxdb

import (
	"context"
	"fmt"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb/pkg/escape"
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

func (s *Source) Close() {
	s.client.Close()
}

type Item struct {
	Value  int
	Mirror string
	Time   time.Time
	Path   string
}

// Result is the return type of Query.
type Result = []Item

func (s *Source) Query(ctx context.Context, cname string) (Result, error) {
	query := fmt.Sprintf(`from(bucket: "%s")
        |> range(start: -15m)
        |> filter(fn: (r) => r._measurement == "repo" and r.name == "%s")
		|> pivot(rowKey:["_time"], columnKey: ["_field"], valueColumn: "_value")
		|> map(fn: (r) => ({
			_value: r.value,
			mirror: r.mirror,
			_time: r._time,
			path: r.url,
			disable: r.disable
		   }))
        |> tail(n: 1)`, s.bucket, escape.String(cname))
	// SQL INJECTION!!! (use read only token)
	res, err := s.queryAPI.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	r := make(Result, 0)
	for res.Next() {
		record := res.Record()
		disable := record.ValueByKey("disable").(bool)
		if !disable {
			r = append(r, Item{
				Value:  int(record.Value().(int64)),
				Mirror: record.ValueByKey("mirror").(string),
				Time:   record.Time(),
				Path:   record.ValueByKey("path").(string),
			})
		}
	}
	return r, res.Err()
}
