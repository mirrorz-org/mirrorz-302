package mirrorzdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func decodeRedirectConfig(data []byte, target any) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("redirect configuration must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// Rewrite maps an escaped path to a replacement template. Compiled expressions
// belong to an immutable configuration snapshot and are safe to share.
type Rewrite struct {
	Match   string `json:"match"`
	Target  string `json:"target"`
	pattern *regexp.Regexp
}

type Redirects struct {
	Rewrite   []Rewrite `json:"rewrite"`
	Whitelist []string  `json:"whitelist"`
	Blacklist []string  `json:"blacklist"`
	whitelist []*regexp.Regexp
	blacklist []*regexp.Regexp
}

func (r *Rewrite) UnmarshalJSON(data []byte) error {
	type plain Rewrite
	return decodeRedirectConfig(data, (*plain)(r))
}

func (r *Redirects) UnmarshalJSON(data []byte) error {
	type plain Redirects
	return decodeRedirectConfig(data, (*plain)(r))
}

type globalRedirects struct {
	Redirects []Rewrite `json:"redirects"`
}

func (g *globalRedirects) UnmarshalJSON(data []byte) error {
	type plain globalRedirects
	return decodeRedirectConfig(data, (*plain)(g))
}

// ValidatePath checks an escaped, repository-relative or site-relative path.
// Decode only for validation: the original spelling is retained for matching
// and output, including encoded slashes and percent signs.
func ValidatePath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("not an absolute path: %q", path)
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.Host != "" || u.Scheme != "" || u.EscapedPath() != path {
		return fmt.Errorf("invalid escaped path: %q", path)
	}
	decoded := u.Path
	if strings.HasPrefix(decoded, "//") || strings.Contains(decoded, `\`) || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid path characters: %q", path)
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("dot segment in path: %q", path)
		}
	}
	return nil
}

// ValidCName reports whether a decoded repository name is a single path segment.
func ValidCName(cname string) bool {
	return cname != "" && cname != "api" && cname != "." && cname != ".." && !strings.ContainsAny(cname, "/\\?#") &&
		strings.IndexFunc(cname, unicode.IsSpace) < 0 && strings.IndexFunc(cname, unicode.IsControl) < 0
}

func (r *Rewrite) compile() error {
	if r.Match == "" || r.Target == "" {
		return fmt.Errorf("rewrite requires match and target")
	}
	var err error
	r.pattern, err = regexp.Compile(`\A(?:` + r.Match + `)\z`)
	if err != nil {
		return err
	}
	// Validate references explicitly: regexp.Expand otherwise silently replaces
	// an unknown capture with an empty string, hiding configuration mistakes.
	var literal strings.Builder
	for i := 0; i < len(r.Target); {
		if r.Target[i] != '$' {
			literal.WriteByte(r.Target[i])
			i++
			continue
		}
		i++
		if i < len(r.Target) && r.Target[i] == '$' {
			literal.WriteByte('$')
			i++
			continue
		}
		start := i
		braced := i < len(r.Target) && r.Target[i] == '{'
		if braced {
			i++
			start = i
		}
		for i < len(r.Target) && (r.Target[i] >= 'a' && r.Target[i] <= 'z' || r.Target[i] >= 'A' && r.Target[i] <= 'Z' || r.Target[i] >= '0' && r.Target[i] <= '9' || r.Target[i] == '_') {
			i++
		}
		name := r.Target[start:i]
		if name == "" || braced && (i == len(r.Target) || r.Target[i] != '}') {
			return fmt.Errorf("invalid capture reference in %q", r.Target)
		}
		if braced {
			i++
		}
		n, numberErr := strconv.Atoi(name)
		if numberErr == nil && (len(name) == 1 || name[0] != '0') {
			if n > r.pattern.NumSubexp() {
				return fmt.Errorf("unknown capture %q", name)
			}
		} else if r.pattern.SubexpIndex(name) < 0 {
			return fmt.Errorf("unknown capture %q", name)
		}
		literal.WriteString("capture")
	}
	return ValidatePath(literal.String())
}

func (r *Redirects) compile() error {
	for i := range r.Rewrite {
		if err := r.Rewrite[i].compile(); err != nil {
			return fmt.Errorf("rewrite[%d]: %w", i, err)
		}
	}
	for _, list := range []struct {
		name   string
		source []string
		target *[]*regexp.Regexp
	}{
		{"whitelist", r.Whitelist, &r.whitelist}, {"blacklist", r.Blacklist, &r.blacklist},
	} {
		for i, expression := range list.source {
			if expression == "" {
				return fmt.Errorf("%s[%d]: empty regex", list.name, i)
			}
			pattern, err := regexp.Compile(expression)
			if err != nil {
				return fmt.Errorf("%s[%d]: %w", list.name, i, err)
			}
			*list.target = append(*list.target, pattern)
		}
	}
	return nil
}

// Apply filters the original path, then applies the first matching rewrite.
// An empty tail represents the repository root, but stays empty if no rewrite
// matches so that unconfigured redirects retain their original trailing slash.
func (r Redirects) Apply(tail string) (string, error) {
	target, _, err := r.ApplyWithRule(tail)
	return target, err
}

// ApplyWithRule also returns the matching rewrite expression for tracing.
func (r Redirects) ApplyWithRule(tail string) (string, string, error) {
	path := tail
	if path == "" {
		path = "/"
	}
	for i, pattern := range r.blacklist {
		if pattern.MatchString(path) {
			return "", "", fmt.Errorf("path blacklist[%d]: %s", i, r.Blacklist[i])
		}
	}
	if len(r.whitelist) > 0 {
		allowed := false
		for _, pattern := range r.whitelist {
			allowed = allowed || pattern.MatchString(path)
		}
		if !allowed {
			return "", "", fmt.Errorf("path not in whitelist")
		}
	}
	for i, rule := range r.Rewrite {
		indices := rule.pattern.FindStringSubmatchIndex(path)
		if indices == nil {
			continue
		}
		target := string(rule.pattern.ExpandString(nil, rule.Target, path, indices))
		if err := ValidatePath(target); err != nil {
			return "", "", fmt.Errorf("rewrite[%d]: %w", i, err)
		}
		return target, rule.Match, nil
	}
	return tail, "", nil
}

// Normalize converts a request path to the global canonical namespace.
func (m *MirrorZDatabase) Normalize(path string) (string, error) {
	if err := ValidatePath(path); err != nil {
		return "", err
	}
	m.mu.RLock()
	rules := m.redirects
	m.mu.RUnlock()
	return rules.Apply(path)
}
