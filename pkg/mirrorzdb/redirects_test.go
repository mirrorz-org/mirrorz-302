package mirrorzdb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteIssueCases(t *testing.T) {
	for _, test := range []struct{ name, match, target, input, output string }{
		{"raspberrypi 169", `^/(.*)$`, `/debian/${1}`, "/dists/bookworm/InRelease", "/debian/dists/bookworm/InRelease"},
		{"raspberrypi root 299", `^/(.*)$`, `/debian/${1}`, "", "/debian/"},
		{"pypi 26 42 131", `^/web/simple(/.*)?$`, `/simple${1}`, "/web/simple/jupyter/", "/simple/jupyter/"},
		{"pypi optional suffix", `^/web/simple(/.*)?$`, `/simple${1}`, "/web/simple", "/simple"},
		{"mysql 138", `^/yum/(mysql-.+-community)-el([0-9]+)-([^/]+)(/.*)?$`, `/yum/${1}/el/${2}/${3}${4}`, "/yum/mysql-connectors-community-el7-x86_64/repodata/repomd.xml", "/yum/mysql-connectors-community/el/7/x86_64/repodata/repomd.xml"},
		{"file at root", `^/$`, `/repo`, "/", "/repo"},
		{"named capture", `^/old/(?P<file>.*)$`, `/new/${file}`, "/old/a%2Fb%25.whl", "/new/a%2Fb%25.whl"},
		{"literal dollar", `^/$`, `/$$file`, "/", "/$file"},
		{"unmatched empty root", `^/old/.*$`, `/new/`, "", ""},
		{"whole match", `/old`, `/new`, "/old/file", "/old/file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := Redirects{Rewrite: []Rewrite{{Match: test.match, Target: test.target}}}
			require.NoError(t, rules.compile())
			actual, err := rules.Apply(test.input)
			require.NoError(t, err)
			assert.Equal(t, test.output, actual)
		})
	}
}

func TestRedirectFilterPrecedence(t *testing.T) {
	rules := Redirects{
		Whitelist: []string{`^/old/`, `^/public/`},
		Blacklist: []string{`^/old/private(/|$)`},
		Rewrite:   []Rewrite{{Match: `^/old/(.*)$`, Target: `/new/${1}`}, {Match: `^/new/(.*)$`, Target: `/again/${1}`}},
	}
	require.NoError(t, rules.compile())
	actual, err := rules.Apply("/old/file")
	require.NoError(t, err)
	assert.Equal(t, "/new/file", actual, "filter input first and do not chain rewrites")
	_, err = rules.Apply("/old/private/file")
	assert.ErrorContains(t, err, "blacklist")
	_, err = rules.Apply("/other/file")
	assert.ErrorContains(t, err, "whitelist")
	actual, err = rules.Apply("/public/file")
	require.NoError(t, err)
	assert.Equal(t, "/public/file", actual)
}

func TestRejectInvalidRedirectConfiguration(t *testing.T) {
	for _, config := range []string{
		`null`, `{"blackist":[".*"]}`, `{"blacklist":".*"}`, `{"blacklist":[""]}`, `{"whitelist":["["]}`,
		`{"rewrite":[null]}`, `{"rewrite":[{"match":"/","target":"/","action":"blackhole"}]}`,
		`{"rewrite":[{"target":"/"}]}`, `{"rewrite":[{"match":"[","target":"/"}]}`,
		`{"rewrite":[{"match":"/","target":"/${missing}"}]}`,
		`{"rewrite":[{"match":"/(.*)","target":"/${2}"}]}`,
		`{"rewrite":[{"match":"/(.*)","target":"/${01}"}]}`,
		`{"rewrite":[{"match":"/","target":"/$"}]}`,
		`{"rewrite":[{"match":"/","target":"/${1"}]}`,
		`{"rewrite":[{"match":"/","target":"https://elsewhere.example/"}]}`,
		`{"rewrite":[{"match":"/","target":"/../other"}]}`,
	} {
		t.Run(config, func(t *testing.T) {
			var rules Redirects
			err := json.Unmarshal([]byte(config), &rules)
			if err == nil {
				err = rules.compile()
			}
			require.Error(t, err)
		})
	}
}

func TestValidateEscapedPath(t *testing.T) {
	for _, path := range []string{"/", "/dists/bookworm/InRelease", "/a%2Fb", "/a%252Fb", "/a%20b", "/%E4%B8%AD%E6%96%87"} {
		assert.NoError(t, ValidatePath(path), path)
	}
	for _, path := range []string{"", "relative", "//host/file", "/a/../b", "/%2e%2e/b", "/a%2f..%2fb", "/%5cfoo", "/%00", "/a?b", "/a#b", "/%zz", "/a b"} {
		assert.Error(t, ValidatePath(path), path)
	}
	rules := Redirects{Rewrite: []Rewrite{{Match: `^/strip/(.*)$`, Target: `/${1}`}}}
	require.NoError(t, rules.compile())
	_, err := rules.Apply("/strip/%2e%2e/other")
	assert.Error(t, err, "validate expanded capture values too")
}

func TestFirstMatchingRewriteWins(t *testing.T) {
	rules := Redirects{Rewrite: []Rewrite{
		{Match: `^/old/(.*)$`, Target: `/first/${1}`},
		{Match: `^/old/.*$`, Target: `/second`},
	}}
	require.NoError(t, rules.compile())
	target, err := rules.Apply("/old/file")
	require.NoError(t, err)
	assert.Equal(t, "/first/file", target)
}

func TestGlobalAndSiteRedirectConfiguration(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "example", `{
		"abbrs": ["EXAMPLE"],
		"endpoints": [{"label": "example", "resolve": "mirrors.example.org"}],
		"redirects": {
			"raspberrypi": {"rewrite": [{"match": "^/(.*)$", "target": "/debian/${1}"}]}
		}
	}`)
	global := []byte(`{"redirects": [
		{"match": "^/raspberrypi/debian(/.*)?$", "target": "/raspberrypi${1}"}
	]}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "redirects.json"), global, 0600))
	db := NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	path, err := db.Normalize("/raspberrypi/debian/dists/bookworm/InRelease")
	require.NoError(t, err)
	assert.Equal(t, "/raspberrypi/dists/bookworm/InRelease", path)
	endpoints, ok := db.Lookup("EXAMPLE")
	require.True(t, ok)
	target, err := endpoints[0].Redirects["raspberrypi"].Apply("/dists/bookworm/InRelease")
	require.NoError(t, err)
	assert.Equal(t, "/debian/dists/bookworm/InRelease", target)
}
