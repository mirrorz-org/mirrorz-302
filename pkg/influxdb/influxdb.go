package influxdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	influxdb "github.com/influxdata/influxdb/client/v2"
)

type Config struct {
	URL      string `json:"url"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`

	// Token, Org and Bucket are retained for compatibility with the old v2 API
	// configuration. Org is not used by the v1 compatibility API.
	Token  string `json:"token"`
	Org    string `json:"org"`
	Bucket string `json:"bucket"`
}

type queryClient interface {
	QueryCtx(context.Context, influxdb.Query) (*influxdb.Response, error)
	Close() error
}

type Source struct {
	database string
	client   queryClient
	err      error
}

// NewSource keeps the old constructor compatible. The token is sent as the
// password of v1 Basic Auth, which InfluxDB 2.x accepts on compatible APIs.
func NewSource(url, token, _ string, bucket string) *Source {
	return newSource(url, bucket, "mirrorz-302", token)
}

func newSource(url, database, username, password string) *Source {
	client, err := influxdb.NewHTTPClient(influxdb.HTTPConfig{
		Addr:     url,
		Username: username,
		Password: password,
	})
	return &Source{database: database, client: client, err: err}
}

func NewSourceFromConfig(config Config) *Source {
	database := config.Database
	if database == "" {
		database = config.Bucket
	}
	username, password := config.Username, config.Password
	if username == "" && password == "" && config.Token != "" {
		username, password = "mirrorz-302", config.Token
	}
	return newSource(config.URL, database, username, password)
}

func (s *Source) Close() {
	if s.client != nil {
		_ = s.client.Close()
	}
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
	if s.err != nil {
		return nil, s.err
	}
	query := influxdb.NewQueryWithParameters(
		`SELECT LAST("value") AS "value", LAST("disable") AS "disable" FROM "repo" WHERE time > now() - 1h AND "name" = $cname GROUP BY "mirror", "url"`,
		s.database,
		"",
		map[string]interface{}{"cname": cname},
	)
	response, err := s.client.QueryCtx(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := response.Error(); err != nil {
		return nil, err
	}

	result := make(Result, 0)
	for _, statement := range response.Results {
		for _, series := range statement.Series {
			for _, values := range series.Values {
				item, disabled, err := itemFromRow(series.Tags, series.Columns, values)
				if err != nil {
					return nil, err
				}
				if !disabled {
					result = append(result, item)
				}
			}
		}
	}
	return result, nil
}

func itemFromRow(tags map[string]string, columns []string, values []interface{}) (Item, bool, error) {
	fields := make(map[string]interface{}, len(columns))
	for i, column := range columns {
		if i < len(values) {
			fields[column] = values[i]
		}
	}

	timestamp, ok := fields["time"].(string)
	if !ok {
		return Item{}, false, fmt.Errorf("InfluxDB result has invalid time %v", fields["time"])
	}
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return Item{}, false, fmt.Errorf("parse InfluxDB result time: %w", err)
	}
	value, err := intValue(fields["value"])
	if err != nil {
		return Item{}, false, err
	}
	disabled, _ := fields["disable"].(bool)

	return Item{
		Value:  value,
		Mirror: tags["mirror"],
		Time:   t,
		Path:   tags["url"],
	}, disabled, nil
}

func intValue(value interface{}) (int, error) {
	switch value := value.(type) {
	case json.Number:
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse InfluxDB integer: %w", err)
		}
		return int(n), nil
	case float64:
		return int(value), nil
	default:
		return 0, fmt.Errorf("InfluxDB result has invalid value %v", value)
	}
}
