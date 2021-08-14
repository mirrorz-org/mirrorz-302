# 302 Backend

At [https://mirrors.mirrorz.org](https://mirrors.mirrorz.org) or [https://m.mirrorz.org](https://m.mirrorz.org) a 302 backend is deployed.

You may visit [https://m.mirrorz.org/archlinux/](https://m.mirrorz.org/archlinux/). Note that only `/${cname}` from the [frontend](https://mirrorz.org/list)/[monitor](https://mirrorz.org/monitor) are valid pathnames.

Currently this is deployed using Cloudflare Workers. Credentials are configured as environment variables.

# 302 Decision

Currently redirecting is decided from information collected by the monitor. Several policies are discussed below. More policies are welcome!

## Newest

This is the current policy. But users may experience low bandwidth.

## Nearest

This is not available now as these meta data (location, ISP, etc) are not provided and collected.

## Random

Not practical, one user may be redirected to a mirror synced several weeks ago, resulting in many 404.

# mirrors.edu.cn

We may use the domain name `mirrors.edu.cn` for providing frontend AND 302 backend service if we have shown enough potential.

## design concern

* user
  - AS: Interconnect within one AS is usually better than across AS
  - IP: From the perspective of CERNET/CERNET2 and universities, mirror site can fine-tune based on IP range
  - GEO: As this project is limited to .edu.cn mirror sites, geographically nearest not necessarily implies nearest in network. Hence this project does not take GEO into concern currently. Also GeoIP data is hard to acquire and maintain, this feature may be added in the future.
  - advanced users may manually assign a preference list, e.g. `tuna-ustccampus.mirrors.edu.cn`
* mirror site
  - endpoint: multiple upstreams (CERNET, CMNET, etc), ipv4/ipv6 only endpoint, and default endpoint
  - range: users inside this range should better be redirected to this mirror site
  - public: private mirror has limited access range, IP/ASN not in its range should not be redirected there
* operator (not implemented)
  - load balance
  - speed testing from multiple AS
  - manually adjust redirection (enable/disable, probability, etc)

## policy

1. The backend checks `Host` in HTTP header, e.g. `tuna6-ustccmcc-ustcchinanet-sjtugsiyuan.mirrors.edu.cn`, which means the user prefers sjtu siyuan server the most, then ustc in chinanet, ustc in cmcc, then tuna in ipv6, then at mirrorz's own wish.
2. According to the repo the user request, one potential list of mirror sites is filtered. Filter by whether this mirror site has the repo (e.g. `/archlinux`), has the content (using search backend, e.g. `/archlinux/sth.iso`) (not implemented!), is syncing or not and is public or not.
3. Rate available mirror sites on the following field: (user preference, speedtest, is in IP range, is in ASN, syncing status).
    1. For example, user requests `mirrors.edu.cn/archlinux` from `166.111.1.1`, AS4538, and TUNA announces one endpoint `tuna` with range `166.111.0.0, AS4538` and TUNA has archlinux not synced for one hour, the score of the request would be (0, 100M, 16, 1, -3600).
    2. First drop all the dominated scores, since they are not better than dominating mirror sites. For example, (1, 1000M, 18, 0, -3600) dominates (0, 100M, 16, 0, -36000). For the detailed definition of domanite, please look it up in the code.
    3. Then "randomly" choose one mirror site as they are "optimal" to some extent.
    4. Especially, when there is only syncing status available for the score of one request, we "randomly" choose one mirror from the optimal half.
    5. "Randomly" does not meaning uniformly randomly, the probability of being chosen can be adjusted by the operator (not implemented, to be implemented).

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
        "AS4809"
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

### Note

* endpoint
  - The first endpoint is the default endpoint. If all the endpoints have the same preference, we choose the first one
  - Each endpoint has many capabilities, which is represented in `filter`.
    + If one user requests mirrorz-302 using `https://mirrors.edu.cn`, we would redirect it to one https site; if `http://mirrors.edu.cn` then one http site. By requesting mirrorz-302 the user has indicated its minimum capability. Normally, redirecting one https user to http site is not safe and vice versa.
    + If one user requests mirrorz-302 using IPv4, we would redirect it to one endpoint with v4 enabled (it is acceptable if v6 is also enabled); if IPv6 then one v6-enabled endpoint. By requesting mirrorz-302 the user has indicated its minimum capability. Normally, redirecting one IPv6 user to IPv4 endpoint is not safe and vice versa.
    + Especially, **advanced** user can explicitly annouce their capability in their request like `http://ssl.mirrors.edu.cn`, then we must redirect it to a https site. Some interesting usage like `https://sjtug-nossl-wsyu-ssl-ustc-tuna.mirrors.edu.cn`, namely no preference (http and https both ok) for sjtug, use http endpoint for wsyu, and force ssl for ustc and tuna.
    + Especially, **advanced** user can explicitly annouce their capability/preference in their request like `4.mirrors.edu.cn`, then we must redirect it to a IPv4 only site. Those with `resolve: "mirrors.example.com", filter: ["V4", "V6"]` is not acceptable for `4.mirrors.edu.cn` as the user client may resolve `mirrors.example.com` with AAAA first, but its IPv6 is broken (common case for most IPv6 enabled edge devices), we must return something like `4.mirrors.example.com`. So for each mirror site, it should add some IPv4 only and IPv6 only endpoint like tuna4 and ustc4 for this special case.
    + Syntax sugar: By default we assume each endpoint has both http and https, hence `resolve: "mirrors.example.com", filter:["NOSSL", "SSL"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. If it has only one ability, like `resolve: "mirrors.example.com", filter:["NOSSL"]` then it can be rewritten into `resolve:"http://mirrors.example.com", filter:[]`. And to be more simple, `resolve:"http://10.10.10.10", filter: []` can be rewritten into `resolve: "10.10.10.10", filter: []` as IP endpoint usually does not have ssl enabled (if enabled, then explicitly use `resolve:"101.6.6.6", filter: ["NOSSL", "SSL"]`).
    + Syntar sugar: By default we assume each endpoint has both A and AAAA, hence `resolve: "mirrors.example.com", filter:["V4", "V6"]` is equivalent to `resolve: "mirrors.example.com", filter:[]`. To be more simple, `resolve:"10.10.10.10", filter: [ "V4" ]` can be rewritten into `resolve: "10.10.10.10", filter: []`. Note that `resolve: "10.10.10.10", filter: ["V6"]` is invalid and the `"V6"` filter will be ignored.
    + Partial capability: One endpoint with `filter: [ "NOSSL", "SSL", "SSL:centos" ]`, namely force SSL for one `cname` called `centos`. If one user requests with `http://mirrors.edu.cn/centos`, this endpoint would not be redirected.
  - Private mirror site may use a private IP but declare a public range. For example, suppose USTC has a private IP range of 10.0.0.0/8, USTC mirror is located at 10.0.0.1:8080, and when one user inside USTC accesses `mirrors.edu.cn`, its IP is NATed into 202.0.0.0/24, then `mirrors.edu.cn` can resolve the request into `ustccampus` endpoint.
* site/mirrors
  - This is used by `mirrors.edu.cn` monitor, while `site` and `mirrors` in `mirrorz.json` is only used by frontend
