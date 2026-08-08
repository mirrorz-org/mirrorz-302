package influxdb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	influxdb "github.com/influxdata/influxdb/client/v2"
	"github.com/influxdata/influxdb/models"
	"github.com/stretchr/testify/require"
)

type fakeQueryClient struct {
	query    influxdb.Query
	response *influxdb.Response
}

func (c *fakeQueryClient) QueryCtx(_ context.Context, query influxdb.Query) (*influxdb.Response, error) {
	c.query = query
	return c.response, nil
}

func (c *fakeQueryClient) Close() error { return nil }

func TestQueryV1API(t *testing.T) {
	client := &fakeQueryClient{response: &influxdb.Response{
		Results: []influxdb.Result{{Series: []models.Row{
			{
				Name:    "repo",
				Tags:    map[string]string{"mirror": "enabled", "url": "/archlinux"},
				Columns: []string{"time", "value", "disable"},
				Values:  [][]interface{}{{"2026-08-08T00:00:00Z", json.Number("-42"), false}},
			},
			{
				Name:    "repo",
				Tags:    map[string]string{"mirror": "disabled", "url": "/archlinux"},
				Columns: []string{"time", "value", "disable"},
				Values:  [][]interface{}{{"2026-08-08T00:00:00Z", json.Number("-1"), true}},
			},
		}}},
	}}
	source := &Source{database: "mirrorz", client: client}

	result, err := source.Query(context.Background(), "archlinux")

	require.NoError(t, err)
	require.Equal(t, "mirrorz", client.query.Database)
	require.Contains(t, client.query.Command, `FROM "repo"`)
	require.Equal(t, "archlinux", client.query.Parameters["cname"])
	require.Equal(t, Result{{
		Value:  -42,
		Mirror: "enabled",
		Time:   time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
		Path:   "/archlinux",
	}}, result)
}

func TestOldV2ConfigCompatibility(t *testing.T) {
	source := NewSourceFromConfig(Config{
		URL:    "http://localhost:8086",
		Token:  "secret",
		Org:    "mirrorz",
		Bucket: "mirrorz",
	})
	defer source.Close()

	require.Equal(t, "mirrorz", source.database)
	require.NoError(t, source.err)
}
