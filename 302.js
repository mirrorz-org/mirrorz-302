addEventListener("fetch", event => {
    event.respondWith(handler(event.request));
});

const query = `
  repo = from(bucket:"mirrorz")
    |> range(start: -10m)
    |> filter(fn: (r) => r._measurement == "repo" and r.name == "reponame")
    |> map(fn: (r) => ({_value:r._value,mirror:r.mirror,_time:r._time,path:r.url}))
    |> tail(n:1)

  site = from(bucket:"mirrorz")
    |> range(start: -10m)
    |> filter(fn: (r) => r._measurement == "site")
    |> map(fn: (r) => ({mirror:r.mirror,url:r.url}))
    |> tail(n:1)

  join(tables: {repo: repo, site: site}, on: ["mirror"])
    |> map(fn: (r) => ({_value:r._value,mirror:r.mirror,url:r.url+r.path,_time:r._time}))
`

function is_debug(request) {
    const params = {};
    const req_url = new URL(request.url);
    const queryString = req_url.search.slice(1).split('&');
    queryString.forEach(item => {
        const kv = item.split('=')
        if (kv[0]) {
            if (kv[0] in params) {
                params[kv[0]].push(kv[1] || true)
            } else {
                params[kv[0]] = [kv[1] || true]
            }
        }
    });
    return ('trace' in params);
}

function newest(parsed) {
    let m = -Infinity;
    let url = null;

    for (const mirror of parsed) {
        try {
            const v = parseInt(mirror.value);
            // 0 is a special value, namely unknown!
            // valid range:  v < 0
            if (v == 0)
                continue;
            if (v > m) {
                m = v;
                url = mirror.url;
            }
        } catch (e) {}
    }
    return url;
}

function decide(parsed) {
    return newest(parsed);
}

function parse_csv(csv) {
    let index_value, index_mirror, index_url;
    let result = [];

    for (const line of csv.split('\r\n')) {
        const arr = line.split(',');
        if (arr.length < 7)
            continue
        if (arr[1] === "result") {
            for (let i = 3; i != 7; ++i) {
                switch(arr[i]) {
                    case "_value":
                        index_value = i;
                        break;
                    case "mirror":
                        index_mirror = i;
                        break;
                    case "url":
                        index_url = i;
                        break;
                }
            }
        } else if (arr[1] === "_result") {
            result.push({
                "value": arr[index_value],
                "url": arr[index_url],
                "mirror": arr[index_mirror],
            });
        }
    }
    return result;
}

async function handler(request) {
    try {
        let pathname = (new URL(request.url)).pathname;
        let pathname_arr = pathname.split('/');

        // Redirect to homepage
        if (pathname_arr[1].length === 0)
            return Response.redirect('https://mirrorz.org/about', 302);

        // Query influxdb 2.x
        response = await fetch(INFLUX_URL, {
            headers: {
                'Authorization': INFLUX_TOKEN,
                'Accept': 'application/csv',
                'Content-Type': 'application/vnd.flux',
            },
            method: "POST",
            body: query.replace('reponame', pathname_arr[1]),
        });

        const csv = await response.text();
        const parsed = parse_csv(csv);
        if (is_debug(request))
            return new Response(JSON.stringify(parsed, null, 2));

        const url = decide(parsed);

        if (url === null)
            return new Response(`Not Found`, {status: 404});

        // Append the remaining path
        let remain_path = pathname.substr(1+pathname_arr[1].length);
        // Dark magic for some sites treating '/archlinux' as file, not directory
        if (remain_path.length === 0 && !pathname.endsWith('/'))
            remain_path = '/'
        const redir_url = url + remain_path;

        return Response.redirect(redir_url, 302);
    } catch (err) {
        return new Response(`${err}`, { status: 500 })
    }
}

