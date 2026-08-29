package mirrorzdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tunaSite = `{
  "abbrs": ["TUNA.NANO", "TUNA.NEO"],
  "endpoints": [{
    "label": "tuna-4",
    "resolve": "mirrors4.tuna.tsinghua.edu.cn",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": ["REGION:BJ", "ISP:CERNET", "166.111.0.0/16"]
  }]
}`

func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	siteDir := filepath.Join(dir, name)
	require.NoError(t, os.Mkdir(siteDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(siteDir, "config.json"), []byte(content), 0o600))
}

func TestLoadSharedEndpoints(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "tuna", tunaSite)

	db := NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	assert.Equal(t, []string{"TUNA.NANO", "TUNA.NEO"}, db.Abbrs())

	for _, abbr := range []string{"TUNA.NANO", "TUNA.NEO"} {
		endpoints, ok := db.Lookup(abbr)
		require.True(t, ok)
		require.Len(t, endpoints, 1)
		assert.Equal(t, "tuna4", endpoints[0].Label)
		assert.Equal(t, "tuna4", endpoints[0].SiteLabel)
		assert.Equal(t, "mirrors4.tuna.tsinghua.edu.cn", endpoints[0].Resolve)
		assert.Equal(t, []string{"BJ"}, endpoints[0].RangeRegion)
		assert.Equal(t, []string{"CERNET"}, endpoints[0].RangeISP)
		require.Len(t, endpoints[0].RangeCIDR, 1)
	}

	resolve, ok := db.ResolveLabel("tuna4")
	assert.True(t, ok)
	assert.Equal(t, "mirrors4.tuna.tsinghua.edu.cn", resolve)
}

func TestLoadRejectsInvalidSiteFiles(t *testing.T) {
	tests := map[string]string{
		"old format":        `{"site":{"abbr":"TUNA"},"endpoints":[{}]}`,
		"missing endpoints": `{"abbrs":["TUNA"]}`,
		"empty abbr":        `{"abbrs":[""],"endpoints":[{}]}`,
		"invalid json":      `{`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, "site", content)
			assert.Error(t, NewMirrorZDatabase().Load(dir))
		})
	}
}

func TestLoadRejectsDuplicateAbbr(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "one", `{"abbrs":["TUNA"],"endpoints":[{}]}`)
	writeConfig(t, dir, "two", `{"abbrs":["TUNA"],"endpoints":[{}]}`)
	assert.ErrorContains(t, NewMirrorZDatabase().Load(dir), "duplicate abbr")
}

func TestFailedReloadKeepsPreviousDatabase(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "tuna", tunaSite)

	db := NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	writeConfig(t, dir, "broken", `{`)
	require.Error(t, db.Load(dir))

	endpoints, ok := db.Lookup("TUNA.NANO")
	assert.True(t, ok)
	assert.Len(t, endpoints, 1)
}

func TestLoadRejectsDirectoryWithoutSiteConfigs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(tunaSite), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "missing-config"), 0o700))

	assert.ErrorContains(t, NewMirrorZDatabase().Load(dir), "no site configurations found")
}
