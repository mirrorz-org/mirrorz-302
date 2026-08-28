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

```json
{
  "abbrs": ["USTC"],
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
        "AS4134",
        "AS4809",
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
  - `public`: the endpoint can be reached outside of its range. Usually `false` for campus-only mirrors. When `public: false` and `range` declares no CIDR, the endpoint is treated as disabled and never serves any request.
  - `private_range` Used **only when `public` is `false`**. It controls access when the client IP does **not** match any CIDR in `range`.
    - **Format**: `[["REGION:...", "ISP:..."], ...]` (a 2D string array).
    - Each inner array is a **group**; all specified conditions inside must match (logical **AND**).
    - The request is allowed if **any** group matches (logical **OR**); otherwise denied.
  - `filter`: Each endpoint has many capabilities
    + `SSL`: HTTPS available
    + `NOSSL`: HTTP available, and does not redirect to HTTPS when accessing repos
    + `V4`: IPv4 available (A record)
    + `V6`: IPv6 available (AAAA record)
  - `range`: when `public`, the endpoint **prefers** these ranges, other user may still use this endpoint; otherwise it **only serves** clients whose IP falls in one of the declared CIDRs. ISP and REGION do not grant access for private endpoints (they are only used for preference scoring when `public`). If no CIDR is declared, the endpoint is disabled.
    + COUNTRY: Must start with `COUNTRY`, then a colon, then [ISO country code](https://en.wikipedia.org/wiki/ISO_3166-1_alpha-2). Example: `COUNTRY:CN` or `COUNTRY:US`. Defaults to `CN`.
    + REGION: Must start with `REGION`, then a colon, then province name (GB/T 2260-2007). Example: `REGION:BJ` (Beijing) or `REGION:SH` (Shanghai). Defaults to `BJ`.
    + ISP: Must start with `ISP`, then a colon, then ISP name. Example: `ISP:CERNET` or `ISP:CHINANET`. Defaults to `CERNET`. All currently supported values are `CERNET`, `CSTNET`, `CHINANET`, `UNICOM` and `CMCC`.
    + ASN (deprecated): Must start with `AS`. Example: `AS4538` and `AS13335`
    + CIDR: Example: `202.0.0.0/24` or `2001:da8::/32`
* `abbrs`
  - Each value must exactly match the `mirror` tag written by mirrorz-monitor. Multiple monitor abbreviations may share the same endpoint configuration.

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
    "COUNTRY:CN",
    "REGION:ZJ",
    "210.32.0.0/20",
    "some other CIDR"
  ]
}
```
The site may use `private_range` to control access for users outside of its `range`. when `public` is `false`, the endpoint will only serve users whose IP falls in its `range`. For users outside CIDRs in `range`, it will check if they match any group in `private_range`. If yes, then the request is allowed; otherwise denied.
As follows, a user from Zhejiang will be possible to be redirected to this endpoint, while a user from Shanghai will be denied.

#### TODO

**Advanced** user can explicitly annouce their capability in their request like `http://ssl.mirrors.edu.cn`, then we must redirect it to a https site. Some interesting usage like `https://sjtug-nossl-wsyu-ssl-ustc-tuna.mirrors.edu.cn`, namely no preference (http and https both ok) for sjtug, use http endpoint for wsyu, and force ssl for ustc and tuna.

**Advanced** user can explicitly annouce their capability/preference in their request like `4.mirrors.edu.cn`, then we must redirect it to a IPv4 only site. Those with `resolve: "mirrors.example.com", filter: ["V4", "V6"]` is not acceptable for `4.mirrors.edu.cn` as the user client may resolve `mirrors.example.com` with AAAA first, but its IPv6 is broken (common case for most IPv6 enabled edge devices), we must return something like `4.mirrors.example.com`. So for each mirror site, it should add some IPv4 only and IPv6 only endpoint like tuna4 and ustc4 for this special case.

Syntax sugar: By default we assume each endpoint has both http and https, hence `resolve: "mirrors.example.com", filter:["NOSSL", "SSL"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. If it has only one ability, like `resolve: "mirrors.example.com", filter:["NOSSL"]` then it can be rewritten into `resolve:"http://mirrors.example.com", filter:[]`. And to be more simple, `resolve:"http://10.10.10.10", filter: []` can be rewritten into `resolve: "10.10.10.10", filter: []` as IP endpoint usually does not have ssl enabled (if enabled, then explicitly use `resolve:"101.6.6.6", filter: ["NOSSL", "SSL"]`).

Syntar sugar: By default we assume each endpoint has both A and AAAA, hence `resolve: "mirrors.example.com", filter:["V4", "V6"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. To be more simple, `resolve:"10.10.10.10", filter: [ "V4" ]` can be rewritten into `resolve: "10.10.10.10", filter: []`. Note that `resolve: "10.10.10.10", filter: ["V6"]` is invalid and the `"V6"` filter will be ignored.

Partial capability: One endpoint with `filter: [ "NOSSL", "SSL", "SSL:centos" ]`, namely force SSL for one `cname` called `centos`. If one user requests with `http://mirrors.edu.cn/centos`, this endpoint would not be redirected.
