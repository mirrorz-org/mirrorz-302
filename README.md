# 302 Backend

We currently have two 302 backend: 302-js and 302-go

302-js is deployed at <https://mirrors.mirrorz.org> or <https://m.mirrorz.org> in short. You may visit <https://m.mirrorz.org/archlinux/>. Note that only `/${cname}` from the [frontend](https://mirrorz.org/list)/[monitor](https://mirrorz.org/monitor) are valid pathnames. Currently this is deployed using Cloudflare Workers. Credentials are configured as environment variables.

302-go is deployed at <https://mirrors.cernet.edu.cn> and <https://mirrors.cngi.edu.cn>. They only redirect to educational mirror sites.

Currently redirecting is decided from information collected by the [monitor](https://github.com/mirrorz-org/mirrorz-monitor). Two policies are discussed and implemented.

# 302-js: Newest

In 302-js, users are just redirected to a mirror site with the most up-to-date info; however, this may not offer enough bandwidth.

# 302-go: Nearest

In 302-go, users are redirected to a mirror site based on their IP, ISP, geolocation etc. Detailed concern is discussed below.

## design concern

* user
  - AS: Interconnect within one AS is usually better than across AS
  - IP: From the perspective of CERNET/CERNET2 and universities, mirror sites can fine-tune based on IP range
  - GEO: As this project is limited to .edu.cn mirror sites, geographical proximity does not necessarily imply fast network connection.
  - advanced users may manually specify a preference list, e.g. `tuna-ustccampus.mirrors.edu.cn`
* mirror site
  - endpoint: multiple upstreams (CERNET, CMNET, etc), ipv4/ipv6 only endpoint, and default endpoint
  - range: users inside this range should better be redirected to this mirror site
  - public: private mirror has limited access range, IP/ASN not in its range should not be redirected there
* operator (not implemented)
  - load balance
  - speed testing from multiple AS
  - manually adjust redirection (enable/disable, probability, etc)

## mirrorz.d.json

Any mirror site participating in **302 backend** should provide this file. Mirror site uses this file to announce their capabilities and restrictions. It is worth noting this file has no conflict with `mirrorz.json` so a mirror site may integrate them together.

```json
{
  "extension": "D",
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
  ],
  "site": { "the same as mirrorz.json/site" },
  "mirrors": [ "the same as mirrorz.json/mirrors" ]
}
```

### Spec

* An endpoint in `endpoints`
  - `label`: a unique identifier for this endpoint
  - `resolve`: a domain name or IP address. This is directly concatenated in the final URL so a subpath may also be provided (e.g. `linux.xidian.edu.cn/mirrors` and `10.0.0.1:8080/proxy`).
    + It should not end with slash `/` as the request path `/archlinux/iso` will be directly concatenated to it.
  - `public`: the endpoint can be reached outside of its range. Usually `false` for campus-only mirrors.
  - `filter`: Each endpoint has many capabilities
    + `SSL`: HTTPS available
    + `NOSSL`: HTTP available
    + `V4`: IPv4 available (A record)
    + `V6`: IPv6 available (AAAA record)
  - `range`: when `public`, the endpoint **prefers** these ranges, other user may still use this endpoint; otherwise it **only serves** these CIDRs/ISPs (Note that GEO is not included)
    + COUNTRY: Must start with `COUNTRY`, then a colon, then [ISO country code](https://en.wikipedia.org/wiki/ISO_3166-1_alpha-2). Example: `COUNTRY:CN` or `COUNTRY:US`. Defaults to `CN`.
    + REGION: Must start with `REGION`, then a colon, then province name (GB/T 2260-2007). Example: `REGION:BJ` (Beijing) or `REGION:SH` (Shanghai). Defaults to `BJ`.
    + ISP: Must start with `ISP`, then a colon, then ISP name. Example: `ISP:CERNET` or `ISP:CHINANET`. Defaults to `CERNET`. All currently supported values are `CERNET`, `CSTNET`, `CHINANET`, `UNICOM` and `CMCC`.
    + ASN (deprecated): Must start with `AS`. Example: `AS4538` and `AS13335`
    + CIDR: Example: `202.0.0.0/24` or `2001:da8::/32`
* site/mirrors
  - This is used by mirrorz-monitor. Defined in `mirrorz.json`.

### Note

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

Usually, the first endpoint is a generic endpoint (e.g. `mirrors.xx.edu.cn`). To make a preference difference, if further endpoint (e.g. `mirrors4` or `cmcc.mirrors`) covers a more specfic `range`, the generic endpoint should not declare these ranges and the redirector should redirect the user to the more specific endpoint.

For example, if `mirrors4` contains some CIDR in its range, e.g. `166.111.0.0`, then we prefer `mirrors4` over `mirrors` when there are requests from that CIDR.

Another example is that for CMCC users, we prefer `cmcc.mirrors` over `mirrors`.

If a user does not match any range or match exactly the same in `mirrors` and `mirrors4`, then we prefer `mirrors`, i.e. the default one.

#### On range when private endpoint

```json
    {
      "label": "ustccampus",
      "public": false,
      "resolve": "10.0.0.1:8080",
      "filter": [ "V4", "NOSSL" ],
      "range": [ "202.0.0.0/24" ]
    },
```

Campus-only mirror site may use a private IP but declare a public range. For example, suppose USTC has a private IP range of 10.0.0.0/8, USTC mirror is located at 10.0.0.1:8080, and when one user inside USTC accesses `mirrors.edu.cn`, its IP is NATed into 202.0.0.0/24, then `mirrors.edu.cn` can resolve the request into `ustccampus` endpoint.

#### TODO

**Advanced** user can explicitly annouce their capability in their request like `http://ssl.mirrors.edu.cn`, then we must redirect it to a https site. Some interesting usage like `https://sjtug-nossl-wsyu-ssl-ustc-tuna.mirrors.edu.cn`, namely no preference (http and https both ok) for sjtug, use http endpoint for wsyu, and force ssl for ustc and tuna.

**Advanced** user can explicitly annouce their capability/preference in their request like `4.mirrors.edu.cn`, then we must redirect it to a IPv4 only site. Those with `resolve: "mirrors.example.com", filter: ["V4", "V6"]` is not acceptable for `4.mirrors.edu.cn` as the user client may resolve `mirrors.example.com` with AAAA first, but its IPv6 is broken (common case for most IPv6 enabled edge devices), we must return something like `4.mirrors.example.com`. So for each mirror site, it should add some IPv4 only and IPv6 only endpoint like tuna4 and ustc4 for this special case.

Syntax sugar: By default we assume each endpoint has both http and https, hence `resolve: "mirrors.example.com", filter:["NOSSL", "SSL"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. If it has only one ability, like `resolve: "mirrors.example.com", filter:["NOSSL"]` then it can be rewritten into `resolve:"http://mirrors.example.com", filter:[]`. And to be more simple, `resolve:"http://10.10.10.10", filter: []` can be rewritten into `resolve: "10.10.10.10", filter: []` as IP endpoint usually does not have ssl enabled (if enabled, then explicitly use `resolve:"101.6.6.6", filter: ["NOSSL", "SSL"]`).

Syntar sugar: By default we assume each endpoint has both A and AAAA, hence `resolve: "mirrors.example.com", filter:["V4", "V6"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. To be more simple, `resolve:"10.10.10.10", filter: [ "V4" ]` can be rewritten into `resolve: "10.10.10.10", filter: []`. Note that `resolve: "10.10.10.10", filter: ["V6"]` is invalid and the `"V6"` filter will be ignored.

Partial capability: One endpoint with `filter: [ "NOSSL", "SSL", "SSL:centos" ]`, namely force SSL for one `cname` called `centos`. If one user requests with `http://mirrors.edu.cn/centos`, this endpoint would not be redirected.
