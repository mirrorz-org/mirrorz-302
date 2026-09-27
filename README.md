# 302 Backend

We currently have two 302 backend: 302-js and 302-go

302-js is deployed at <https://mirrors.mirrorz.org> or <https://m.mirrorz.org> in short. You may visit <https://m.mirrorz.org/archlinux/>. Note that only `/${cname}` from the [frontend](https://mirrorz.org/list)/[monitor](https://mirrorz.org/monitor) are valid pathnames. Currently this is deployed using Cloudflare Workers. Credentials are configured as environment variables.

302-go is deployed at <https://mirrors.cernet.edu.cn>. They only redirect to educational mirror sites.

Currently redirecting is decided from information collected by the [monitor](https://github.com/mirrorz-org/mirrorz-monitor). Two policies are discussed and implemented.

# 302-js: Newest

**302-js is no longer supported.**

In 302-js, users are just redirected to a mirror site with the most up-to-date info; however, this may not offer enough bandwidth.

# 302-go: Nearest

In 302-go, users are redirected to a mirror site based on their IP, ISP, geolocation etc. Detailed concern is discussed below.

## Runtime signals

The running service handles the following signals:

* `SIGHUP` reloads the site and endpoint configuration from
  `mirrorz-d-directory`. The new configuration replaces the live database only
  after it has been loaded successfully; otherwise the previous configuration
  remains active.
* `SIGUSR1` reads the main YAML configuration file, but currently does not apply
  the result to the running server. Restart `mirrorzd` to apply changes to the
  main configuration.
* `SIGUSR2` reopens log files and is intended for use by log rotation.
* `SIGWINCH` clears the redirect result cache.

To reload only the site configuration under systemd, run:

```shell
systemctl reload mirrorzd.service
```

The supplied unit implements this by sending `SIGHUP` to its main process. Do
not implement reload by rapidly sending both `SIGUSR1` and `SIGHUP`: the main
configuration would still not be applied, and the process does not guarantee
that both signals will be handled.

## design concern

* user
  - AS: Interconnect within one AS is usually better than across AS
  - IP: From the perspective of CERNET/CERNET2 and universities, mirror sites can fine-tune based on IP range
  - GEO: As this project is limited to .edu.cn mirror sites, geographical proximity does not necessarily imply fast network connection.
  - advanced users may manually specify a preference list, e.g. `tuna-ustccampus.mirrors.edu.cn`
    - (experimental) or mix in undesired sites like, e.g. `avoidustccampus`. `avoid` + an endpoint's label excludes that endpoint from candidates; `avoid` + a site's representative label (the first endpoint's label) excludes the whole site. An avoided endpoint is fully excluded rather than just deprioritized.
* mirror site
  - endpoint: multiple upstreams (CERNET, CMNET, etc), ipv4/ipv6 only endpoint, and default endpoint
  - range: users inside this range should better be redirected to this mirror site
  - public: private mirror has limited access range, IP not in its CIDR range should not be redirected there. A private mirror must declare at least one CIDR in `range`, otherwise it is treated as disabled.
* operator (not implemented)
  - load balance
  - speed testing from multiple AS
  - manually adjust redirection (enable/disable, probability, etc)

## Site configuration

The redirector loads static site and endpoint configuration from the directory
set by `mirrorz-d-directory`. Repository availability, paths, status, and
freshness are supplied separately by mirrorz-monitor through InfluxDB.
Each immediate subdirectory represents one site and contains its endpoint
configuration in `config.json`, for example `sites/ustc/config.json`.

```json
{
  "abbrs": ["USTC"],
  "blacklist": [],
  "whitelist": [],
  "endpoints": [
    {
      "label": "ustc",
      "public": true,
      "resolve": "mirrors.ustc.edu.cn",
      "filter": [ "V4", "V6", "SSL", "NOSSL" ],
      "range": []
    },
    {
      "label": "ustc6",
      "public": true,
      "resolve": "ipv6.mirrors.ustc.edu.cn",
      "filter": [ "V6", "SSL", "NOSSL" ],
      "range": []
    },
    {
      "label": "ustcchinanet",
      "public": true,
      "resolve": "chinanet.mirrors.ustc.edu.cn",
      "filter": [ "V4", "SSL", "NOSSL" ],
      "range": [
        "REGION:AH",
        "ISP:CHINANET"
      ]
    },
    {
      "label": "ustccampus",
      "public": false,
      "resolve": "10.0.0.1:8080/proxy",
      "filter": [ "V4", "NOSSL" ],
      "range": [
        "202.0.0.0/24",
        "2001:da8::/32"
      ]
    }
  ]
}
```

### Spec

* An endpoint in `endpoints`
  - `label`: a unique identifier for this endpoint
  - `resolve`: a domain name or IP address. This is directly concatenated in the final URL so a subpath may also be provided (e.g. `linux.xidian.edu.cn/mirrors` and `10.0.0.1:8080/proxy`).
    + It should not end with slash `/` as the request path `/archlinux/iso` will be directly concatenated to it.
  - `public`: when `true`, `range` only affects preference scoring. When `false`, matching CIDR entries in `range` can access it, and users outside those CIDRs may still be allowed by `private_range`. If `range` has no CIDR, access is determined by `private_range`.
  - `private_range`: used **only when `public` is `false`**. It controls access for clients whose IP does **not** match any CIDR in `range`, and can also be the only access rule when `range` has no CIDR.
    - **Format**: `[["REGION:...", "ISP:..."], ...]` (a 2D string array).
    - Each inner array is a **group**; all specified conditions inside must match (logical **AND**).
    - The request is allowed if **any** group matches (logical **OR**); otherwise denied.
    - Each group may contain at most one `REGION` and one `ISP`. Empty groups, empty values, duplicate conditions, and unknown condition types make the site configuration invalid.
    - REGION/ISP access requires a successful lookup from a loaded IPDB. If geolocation is unavailable, `private_range` does not grant access.
  - `filter`: Each endpoint has many capabilities
    + `SSL`: HTTPS available
    + `NOSSL`: HTTP available, and does not redirect to HTTPS when accessing repos
    + `V4`: IPv4 available (A record)
    + `V6`: IPv6 available (AAAA record)
  - `range`: describes endpoint preferences and CIDR access rules. For a public endpoint, matching REGION, ISP, or CIDR entries improve its score, but non-matching clients may still use it. For a private endpoint, a matching CIDR grants access directly; REGION and ISP entries may affect scoring after the endpoint is eligible, but do not grant access. If no CIDR matches, `private_range` may still grant access.
    + REGION: Must start with `REGION`, then a colon, then a province code (GB/T 2260-2007). Example: `REGION:BJ` (Beijing) or `REGION:SH` (Shanghai).
    + ISP: Must start with `ISP`, then a colon, then an ISP name. Example: `ISP:CERNET` or `ISP:CHINANET`. Supported values are `CERNET`, `CSTNET`, `CHINANET`, `UNICOM` and `CMCC`.
    + CIDR: Example: `202.0.0.0/24` or `2001:da8::/32`
* `abbrs`
  - Each value must exactly match the `mirror` tag written by mirrorz-monitor. Multiple monitor abbreviations may share the same endpoint configuration.
* `blacklist` / `whitelist`
  - Optional arrays of repository cnames, applied to every endpoint and abbreviation
    of this site. Matching is exact and case-sensitive, using the cname (e.g.
    `"debian"`), not the mirror's repository path or a glob pattern.
  - `blacklist` excludes listed repositories. A non-empty `whitelist` allows only
    listed repositories. Omitted, `null`, or empty lists impose no restriction.
    If a repository appears in both lists, the blacklist wins.
  - For example, `"blacklist": ["ubuntu"]` blocks Ubuntu, while
    `"whitelist": ["debian", "debian-cd"]` allows only those two repositories.
    These rules apply to redirects and APT/RPM mirror lists, including explicit
    endpoint preferences. `/api/scoring` without a cname still lists the site.
  - Reload the site configuration with `SIGHUP` to apply changes and clear cached
    redirect results.

### Path rewrites and availability

Put global rules in `redirects.json` directly inside `mirrorz-d-directory`
(normally `mirrorz-config/sites/redirects.json`). They match the complete
request path before the cname is selected:

```json
{
  "redirects": [
    {"match": "^/raspberrypi/debian(/.*)?$", "target": "/raspberrypi${1}"}
  ]
}
```

Site `config.json` files can add rules keyed by the resulting canonical cname:

```json
{
  "redirects": {
    "raspberrypi": {
      "rewrite": [{"match": "^/(.*)$", "target": "/debian/${1}"}]
    },
    "openwrt": {
      "blacklist": ["^/releases/24\\.10\\.6(/|$)"]
    }
  }
}
```

Site rules match paths **relative to the repository root**. For example,
`/raspberrypi/dists/bookworm/InRelease` supplies `/dists/bookworm/InRelease` to
the site rule. If the monitor reports `/archive.raspberrypi.org`, the result
is `/archive.raspberrypi.org/debian/dists/bookworm/InRelease`. Endpoint base
paths and absolute repository URLs keep their existing meaning.

* `rewrite` is an ordered array of `match` regexes and `target` replacement
  templates. Matching uses Go's `regexp` syntax and must cover the whole path.
  The first match wins; rewrites do not chain. Targets support `${1}` and
  `${name}` captures, and `$$` for a literal dollar sign. They replace the
  entire matched path; include a capture to preserve its suffix.
* `blacklist` and `whitelist` are arrays of regexes tested against the standard
  repository-relative path **before** rewriting. A blacklist match excludes
  the site for this request. A non-empty whitelist requires at least one
  match. Blacklist wins, including over endpoint preferences. Unlike rewrite
  matches, these regexes may match part of the path; use anchors as needed.
* Path exclusion means the site lacks this data. Other sites are still tried;
  404 is returned if none qualify. Existing top-level cname blacklists and
  whitelists continue to apply independently.
* Paths use escaped URL spelling, including `%2F` and `%25`. Query strings are
  not matched or changed. A repository root is matched as `/`, including a
  request without a trailing slash. Without a matching rewrite, its original
  trailing-slash behavior is preserved. Targets must start with `/` and may
  not contain an authority, query, fragment, dot segments, or control characters.
* A missing global file or omitted/empty rule collections leave requests
  unchanged. Invalid regexes, unknown capture references, and unknown rule
  fields reject the configuration. Global and site rules reload atomically
  with `SIGHUP`; a failed reload retains the old rules and cache.

APT/RPM mirrorlists deliberately ignore path blacklists and whitelists: the
client handles missing data. A site with `rewrite` rules for a cname must
add `"mirrorlist_paths": {"raspberrypi": "/raspberrypi/debian"}` at the site
configuration's top level to appear in that repository's lists. These paths
are complete repository roots relative to the endpoint, and declare that the
client can append repository paths directly. Arbitrary file rewrites cannot
be represented by a mirrorlist. RPM appends its requested directory to the
declared root; a blacklist does not remove a site with a declared root.

Global normalization also applies to the repository portion of mirrorlist
and scoring API requests. The cache shares monitor data by canonical cname
and client preferences; each request applies its own path rules and scoring.
`?trace=1` shows canonical paths, path exclusions, and the final target.

The deployed configuration belongs in mirrorz-config: global rules in
`sites/redirects.json` and site rules in `sites/<site>/config.json`.
The OpenWrt blacklist above illustrates a historical missing release, not a
current recommendation to exclude that release.

### Note

#### Endpoints for debugging

* Add `?trace=1` to print available site score for selected repo:

   ```shell
   curl -4 -v 'https://mirrors.cernet.edu.cn/ubuntu/?trace=1'
   curl -6 -v 'https://mirrors.cernet.edu.cn/debian/?trace=1'
   ```

* `/api/scoring` to print all available sites

   ```shell
   curl https://mirrors.cernet.edu.cn/api/scoring | jq .
   ```

#### Package-manager mirror lists

The Go backend exposes separate mirror-list formats for APT and RPM clients.
Both lists contain the best eligible endpoint from each site in scoring order
and omit duplicate repository URLs. Endpoint selection depends on the client's
network and preference labels; ties favor the endpoint listed first in the site
configuration. Other endpoints from the same site are omitted, so clients fall
back to other listed sites if the selected endpoint fails.

APT 1.6 or newer can use the APT-specific list through the `mirror+https`
transport. For example, replace `debian` with the repository cname where
necessary:

```text
deb mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/debian bookworm main
```

The equivalent deb822 source is:

```text
Types: deb
URIs: mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/debian
Suites: bookworm
Components: main
```

The APT response assigns `priority:1` to the highest-scoring URL and increasing
priority numbers to the remaining fallback URLs.

For Debian security and Ubuntu security updates, add `?official_index=1` to
prefer official indexes while downloading packages from scored mirrors:

```text
deb mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/debian-security?official_index=1 bookworm-security main
deb mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/ubuntu?official_index=1 noble-security main restricted universe multiverse
deb mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/ubuntu-ports?official_index=1 noble-security main restricted universe multiverse
```

The equivalent deb822 entries (use the entry for your distribution) are:

```text
Types: deb
URIs: mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/debian-security?official_index=1
Suites: bookworm-security
Components: main

Types: deb
URIs: mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/ubuntu?official_index=1
Suites: noble-security
Components: main restricted universe multiverse

Types: deb
URIs: mirror+https://mirrors.cernet.edu.cn/api/apt/mirrorlist/ubuntu-ports?official_index=1
Suites: noble-security
Components: main restricted universe multiverse
```

Keep Ubuntu security in a separate source entry. Regular, updates, and backports
suites should continue using the URL without this parameter; the server does not
inspect the suites configured in APT. This mode supports `debian-security`,
`ubuntu`, and `ubuntu-ports`. An unsupported repository
with `official_index=1` returns HTTP 400. Other parameter values leave the list
unchanged.

The list starts with the corresponding official URL:
`https://security.debian.org/debian-security/`,
`https://security.ubuntu.com/ubuntu/`, or `https://ports.ubuntu.com/ubuntu-ports/`,
tagged `priority:0 type:index`, followed by
the eligible mirrors with priorities starting at 1. The official URL appears
again after all mirrors as the final fallback without a type restriction.
Packages therefore prefer mirrors and fall back to the official server if, for
example, mirrors have not synchronized a new package yet. If the official index
cannot be fetched, APT may try mirror indexes too. Without eligible mirrors the
list contains only the two official entries. Monitor failures reuse stale cached
data when available, otherwise this mode still returns the official entries.

DNF and DNF5 can use the RPM-specific endpoint as a regular `mirrorlist`.
RPM repository variables are expanded by DNF before it requests the list, so a
repository-specific path can follow the cname. For example:

```ini
[rocky-baseos]
name=Rocky Linux BaseOS
mirrorlist=https://mirrors.cernet.edu.cn/api/rpm/mirrorlist/rocky/$releasever/BaseOS/$basearch/os
enabled=1
gpgcheck=1
```

The RPM response is a plain URL-per-line list in scoring order. DNF normally
uses this as its initial order, but `fastestmirror=True` deliberately overrides
the server-provided order. The server uses only the cname (`rocky` above) to
query and cache monitor data, then safely appends the expanded repository path to
each URL. RPM metalink output is not currently provided.

Both endpoints accept `GET` and `HEAD`. Query parameters such as DNF's
`countme` are accepted but are not copied into the returned repository URLs.

Repository mirrors whose monitor delta is more negative than either the dynamic
outlier cutoff or `max-repo-staleness` are excluded from scoring. The latter is
configured in seconds and defaults to 172800 (48 hours).

Monitor results are sorted by their latest data timestamp. A mirror whose data
is at least five minutes older than the newest mirror is treated as offline and
excluded. This comparison is relative so a monitor-wide collection outage does
not make every mirror unavailable.

#### On range when multiple endpoints

```json
    {
      "label": "ustc",
      "public": true,
      "resolve": "mirrors.ustc.edu.cn",
      "filter": [ "V4", "V6", "SSL", "NOSSL" ],
      "range": [ " ISP:CMCC should not be included here as we already have a more specified endpoint. " ]
    },
    {
      "label": "ustccmcc",
      "public": true,
      "resolve": "cmcc.mirrors.ustc.edu.cn",
      "filter": [ "V4", "SSL", "NOSSL" ],
      "range": [ "ISP:CMCC" ]
    },
```

The first endpoint is the default endpoint. If all the endpoints have the same preference, we choose the first one.

Usually, the first endpoint is a generic (representative) endpoint (e.g. `mirrors.xx.edu.cn`). To make a preference difference, if further endpoint (e.g. `mirrors4` or `cmcc.mirrors`) covers a more specfic `range`, the generic endpoint should not declare these ranges and the redirector should redirect the user to the more specific endpoint.

For example, if `mirrors4` contains some CIDR in its range, e.g. `166.111.0.0`, then we prefer `mirrors4` over `mirrors` when there are requests from that CIDR.

Another example is that for CMCC users, we prefer `cmcc.mirrors` over `mirrors`.

If a user does not match any range or match exactly the same in `mirrors` and `mirrors4`, then we prefer `mirrors`, i.e. the default one.

#### On range when private endpoint

```json
{
  "label": "zju",
  "public": false,
  "resolve": "mirrors.zju.edu.cn",
  "filter": ["V4", "V6", "SSL", "NOSSL"],
  "private_range": [
    ["REGION:ZJ"]
  ],
  "range": [
    "210.32.0.0/20"
  ]
}
```

The site may use `private_range` to control access for users outside of its CIDR `range`. When `public` is `false`, CIDR matches are allowed directly. For users outside those CIDRs, each `private_range` group is checked. In this example, clients in `210.32.0.0/20` are allowed directly, and other clients are allowed only when the IPDB identifies them as being in Zhejiang.

#### TODO

**Advanced** user can explicitly annouce their capability in their request like `http://ssl.mirrors.edu.cn`, then we must redirect it to a https site. Some interesting usage like `https://sjtug-nossl-wsyu-ssl-ustc-tuna.mirrors.edu.cn`, namely no preference (http and https both ok) for sjtug, use http endpoint for wsyu, and force ssl for ustc and tuna.

**Advanced** user can explicitly annouce their capability/preference in their request like `4.mirrors.edu.cn`, then we must redirect it to a IPv4 only site. Those with `resolve: "mirrors.example.com", filter: ["V4", "V6"]` is not acceptable for `4.mirrors.edu.cn` as the user client may resolve `mirrors.example.com` with AAAA first, but its IPv6 is broken (common case for most IPv6 enabled edge devices), we must return something like `4.mirrors.example.com`. So for each mirror site, it should add some IPv4 only and IPv6 only endpoint like tuna4 and ustc4 for this special case.

Syntax sugar: By default we assume each endpoint has both http and https, hence `resolve: "mirrors.example.com", filter:["NOSSL", "SSL"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. If it has only one ability, like `resolve: "mirrors.example.com", filter:["NOSSL"]` then it can be rewritten into `resolve:"http://mirrors.example.com", filter:[]`. And to be more simple, `resolve:"http://10.10.10.10", filter: []` can be rewritten into `resolve: "10.10.10.10", filter: []` as IP endpoint usually does not have ssl enabled (if enabled, then explicitly use `resolve:"101.6.6.6", filter: ["NOSSL", "SSL"]`).

Syntar sugar: By default we assume each endpoint has both A and AAAA, hence `resolve: "mirrors.example.com", filter:["V4", "V6"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. To be more simple, `resolve:"10.10.10.10", filter: [ "V4" ]` can be rewritten into `resolve: "10.10.10.10", filter: []`. Note that `resolve: "10.10.10.10", filter: ["V6"]` is invalid and the `"V6"` filter will be ignored.

Partial capability: One endpoint with `filter: [ "NOSSL", "SSL", "SSL:centos" ]`, namely force SSL for one `cname` called `centos`. If one user requests with `http://mirrors.edu.cn/centos`, this endpoint would not be redirected.
