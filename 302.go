package main

import (
    "fmt"
    "strings"
    "net"
    "net/http"
    "math/rand"
    "time"

    "context"
    "github.com/influxdata/influxdb-client-go/v2"
    "github.com/influxdata/influxdb-client-go/v2/api"

    "encoding/json"
    "io/ioutil"
    "path/filepath"

    "flag"

    "github.com/juju/loggo"

    "sort"

    "os/signal"
    "os"
    "syscall"
)

type Config struct {
    InfluxDBURL string `json:"influxdb-url"`
    InfluxDBToken string `json:"influxdb-token"`
    InfluxDBBucket string `json:"influxdb-bucket"`
    InfluxDBOrg string `json:"influxdb-org"`
    IPASNURL string `json:"ipasn-url"`
    HTTPBindAddress string `json:"http-bind-address"`
    MirrorZDDirectory string `json:"mirrorz-d-directory"`
    Homepage string `json:"homepage"`
    DomainLength int `json:"domain-length"`
    CacheTime int64 `json:"cache-time"`
}

var logger = loggo.GetLogger("mirrorzd")
var config Config

var client influxdb2.Client
var queryAPI api.QueryAPI

func LoadConfig (path string, debug bool) (err error) {
    if debug {
        loggo.ConfigureLoggers("mirrorzd=DEBUG")
    } else {
        loggo.ConfigureLoggers("mirrorzd=INFO")
    }

    file, err := ioutil.ReadFile(path)
    if (err != nil) {
        logger.Errorf("LoadConfig ReadFile failed: %v\n", err)
        return
    }
    err = json.Unmarshal([]byte(file), &config)
    if (err != nil) {
        logger.Errorf("LoadConfig json Unmarshal failed: %v\n", err)
        return
    }
    if (config.InfluxDBToken == "") {
        logger.Errorf("LoadConfig find no InfluxDBToken in file\n")
        return
    }
    if (config.InfluxDBURL == "") {
        config.InfluxDBURL = "http://localhost:8086"
    }
    if (config.InfluxDBBucket == "") {
        config.InfluxDBBucket = "mirrorz"
    }
    if (config.InfluxDBOrg == "") {
        config.InfluxDBOrg = "mirrorz"
    }
    if (config.IPASNURL == "") {
        config.IPASNURL = "http://localhost:8889"
    }
    if (config.HTTPBindAddress == "") {
        config.HTTPBindAddress = "localhost:8888"
    }
    if (config.MirrorZDDirectory == "") {
        config.MirrorZDDirectory = "mirrorz.d"
    }
    if (config.Homepage == "") {
        config.Homepage = "mirrorz.org"
    }
    if (config.DomainLength == 0) {
        // 4 for *.mirrors.edu.cn
        // 4 for *.m.mirrorz.org
        // 5 for *.mirrors.cngi.edu.cn
        // 5 for *.mirrors.cernet.edu.cn
        config.DomainLength = 5
    }
    if (config.CacheTime == 0) {
        config.CacheTime = 300
    }
    logger.Debugf("LoadConfig InfluxDB URL: %s\n", config.InfluxDBURL)
    logger.Debugf("LoadConfig InfluxDB Org: %s\n", config.InfluxDBOrg)
    logger.Debugf("LoadConfig InfluxDB Bucket: %s\n", config.InfluxDBBucket)
    logger.Debugf("LoadConfig IPASN URL: %s\n", config.IPASNURL)
    logger.Debugf("LoadConfig HTTP Bind Address: %s\n", config.HTTPBindAddress)
    logger.Debugf("LoadConfig MirrorZ D Directory: %s\n", config.MirrorZDDirectory)
    logger.Debugf("LoadConfig Homepage: %s\n", config.Homepage)
    logger.Debugf("LoadConfig Domain Length: %d\n", config.DomainLength)
    logger.Debugf("LoadConfig Cache Time: %d\n", config.CacheTime)
    return
}

var AbbrToEndpoints map[string][]EndpointInternal
var LabelToResolve map[string]string

func Handler(w http.ResponseWriter, r *http.Request) {
    // [1:] for no heading `/`
    pathArr := strings.SplitN(r.URL.Path[1:], "/", 2)

    cname := ""
    tail := ""
    if r.URL.Path == "/"  {
        labels := Host(r)
        scheme := Scheme(r)
        if len(labels) != 0 {
            resolve, ok := LabelToResolve[labels[len(labels)-1]]
            if ok {
                http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, resolve), http.StatusFound)
                return
            }
        }
        http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, config.Homepage), http.StatusFound)
        return
    } else {
        cname = pathArr[0]
        if len(pathArr) == 2 {
            tail = "/" + pathArr[1]
        }
    }

    _, trace := r.URL.Query()["trace"]

    url, traceStr, err := Resolve(r, cname, trace)

    if trace {
        fmt.Fprintf(w, "%s", traceStr)
    } else if url == "" || err != nil {
        http.NotFound(w, r)
    } else {
        http.Redirect(w, r, fmt.Sprintf("%s%s", url, tail), http.StatusFound)
    }
}

func Scheme (r *http.Request) (scheme string) {
    scheme = r.Header.Get("X-Forwarded-Proto")
    if (scheme == "") {
        scheme = "https"
    }
    return
}

func IP (r *http.Request) (ip net.IP) {
    ip = net.ParseIP(r.Header.Get("X-Real-IP"))
    return
}

func ASN (ip net.IP) (asn string) {
    client := http.Client {
        Timeout: 500 * time.Millisecond,
    }
    req := config.IPASNURL + "/" + ip.String()
    resp, err := client.Get(req)
    if err != nil {
        logger.Errorf("IPASN HTTP Get failed: %v\n", err)
        return
    }
    defer resp.Body.Close()
    body, err := ioutil.ReadAll(resp.Body)
    asn = string(body)
    return
}

func Host (r *http.Request) (labels []string) {
    dots := strings.Split(r.Header.Get("X-Forwarded-Host"), ".")
    if (len(dots) != config.DomainLength) {
        return
    }
    labels = strings.Split(dots[0], "-")
    return
}

type Score struct {
    pos int // pos of label, bigger the better
    mask int // maximum mask
    as int // is in
    delta int // often negative

    // payload
    resolve string
    repo string
}

func (l Score) Less(r Score) bool {
    // ret > 0 means r > l
    if (l.pos != r.pos) {
        return r.pos - l.pos < 0
    }
    if (l.mask != r.mask) {
        return r.mask - l.mask < 0
    }
    if (l.as != r.as) {
        if (l.as == 1) {
            return true
        } else {
            return false
        }
    }
    if (l.delta == 0) {
        return false
    } else if (r.delta == 0) {
        return true
    } else if (l.delta < 0 && r.delta > 0) {
        return true
    } else if (r.delta < 0 && l.delta > 0) {
        return false
    } else if (r.delta > 0 && l.delta > 0) {
        return l.delta - r.delta <= 0
    } else {
        return r.delta - l.delta <= 0
    }
}

func (l Score) DominateExceptDelta(r Score) bool {
    rangeDominate := false
    if l.mask > r.mask || (l.mask == r.mask && l.as >= r.as && r.as != 1) {
        rangeDominate = true
    }
    return l.pos >= r.pos && rangeDominate
}

func (l Score) Dominate(r Score) bool {
    deltaDominate := false
    if l.delta == 0 && r.delta == 0 {
        deltaDominate = true
    } else if l.delta < 0 && r.delta < 0 && l.delta > r.delta {
        deltaDominate = true
    } else if l.delta > 0 && r.delta > 0 && l.delta < r.delta {
        deltaDominate = true
    }
    return l.DominateExceptDelta(r) && deltaDominate
}

func (l Score) DeltaOnly() bool {
    return l.pos == 0 && l.mask == 0 && l.as == 0
}

func (l Score) EqualExceptDelta(r Score) bool {
    return l.pos == r.pos && l.mask == r.mask && l.as == r.as
}

type Scores []Score

func (s Scores) Len() int { return len(s) }

func (s Scores) Less(l, r int) bool {
    return s[l].Less(s[r])
}

func (s Scores) Swap(l, r int) { s[l], s[r] = s[r], s[l] }

func (scores Scores) OptimalsExceptDelta() (optimalScores Scores) {
    for i, l := range scores {
        dominated := false
        for j, r := range scores {
            if i != j && r.DominateExceptDelta(l) {
                dominated = true
            }
        }
        if !dominated {
            optimalScores = append(optimalScores, l)
        }
    }
    return
}

func (scores Scores) Optimals() (optimalScores Scores) {
    for i, l := range scores {
        dominated := false
        for j, r := range scores {
            if i != j && r.Dominate(l) {
                dominated = true
            }
        }
        if !dominated {
            optimalScores = append(optimalScores, l)
        }
    }
    return
}

func (scores Scores) AllDelta() (allDelta bool) {
    allDelta = true
    for _, s := range scores {
        if !s.DeltaOnly() {
            allDelta = false
        }
    }
    return
}

func (scores Scores) AllEqualExceptDelta() (allEqualExceptDelta bool) {
    allEqualExceptDelta = true
    if len(scores) == 0 {
        return
    }
    for _, l := range scores {
        if !l.EqualExceptDelta(scores[0]) { // [0] valid ensured by previous if
            allEqualExceptDelta = false
        }
    }
    return
}

func (scores Scores) RandomRange(r int) (score Score) {
    i := rand.Intn(r)
    score = scores[i]
    return
}

func (scores Scores) RandomHalf() (score Score) {
    score = scores.RandomRange((len(scores)+1)/2)
    return
}

func (scores Scores) Random() (score Score) {
    score = scores.RandomRange(len(scores))
    return
}


// IP, label to start, last timestamp, url
type Resolved struct {
    start int64 // starting timestamp, namely still check db after some time
    last int64  // last update timestamp
    url string
    resolve string // only used in resolveExist
}

var resolved map[string]Resolved

func Resolve(r *http.Request, cname string, trace bool) (url string, traceStr string, err error) {
    traceFunc := func(s string) {
        logger.Debugf(s)
        if trace {
            traceStr += s
        }
    }

    labels := Host(r)
    remoteIP := IP(r)
    asn := ASN(remoteIP)
    scheme := Scheme(r)
    traceFunc(fmt.Sprintf("labels: %v\n", labels))
    traceFunc(fmt.Sprintf("IP: %v\n", remoteIP))
    traceFunc(fmt.Sprintf("ASN: %s\n", asn))
    traceFunc(fmt.Sprintf("Scheme: %s\n", scheme))

    // check if already resolved / cached
    key := strings.Join([]string{
        remoteIP.String(),
        cname,
        asn,
        scheme,
        strings.Join(labels, "-"),
    }, "+")
    keyResolved, prs := resolved[key]

    // all valid, use cached result
    cur := time.Now().Unix()
    if prs &&
            cur - keyResolved.last < config.CacheTime &&
            cur - keyResolved.start < config.CacheTime {
        url = keyResolved.url
        // update timestamp
        resolved[key] = Resolved {
            start: keyResolved.start,
            last: cur,
            url: url,
            resolve: keyResolved.resolve,
        }
        cachedLog := fmt.Sprintf("Cached: %s (%v, %s) %v\n", url, remoteIP, asn, labels)
        traceFunc(cachedLog)
        logger.Infof(cachedLog)
        return
    }

    query := fmt.Sprintf(`from(bucket:"%s")
        |> range(start: -15m)
        |> filter(fn: (r) => r._measurement == "repo" and r.name == "%s")
        |> map(fn: (r) => ({_value:r._value,mirror:r.mirror,_time:r._time,path:r.url}))
        |> tail(n:1)`, config.InfluxDBBucket, cname)
    // SQL INJECTION!!! (use read only token)

    res, err := queryAPI.Query(context.Background(), query)

    if (err != nil) {
        logger.Errorf("Resolve query: %v\n", err)
        return
    }

    var resolve string
    var repo string

    if prs &&
            cur - keyResolved.last < config.CacheTime &&
            cur - keyResolved.start >= config.CacheTime {
        resolve, repo = ResolveExist(res, &traceStr, trace, keyResolved.resolve)
    }

    if resolve == "" && repo == "" {
        // the above IF does not hold or resolveNotExist
        resolve, repo = ResolveBest(res, &traceStr, trace, labels, remoteIP, asn, scheme)
    }

    if resolve == "" && repo == "" {
        url = ""
    } else if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
        url = repo
    } else {
        url = fmt.Sprintf("%s://%s%s", scheme, resolve, repo)
    }
    resolved[key] = Resolved {
        start: cur,
        last: cur,
        url: url,
        resolve: resolve,
    }
    resolvedLog := fmt.Sprintf("Resolved: %s (%v, %s) %v\n", url, remoteIP, asn, labels)
    traceFunc(resolvedLog)
    logger.Infof(resolvedLog)
    return
}

func ResolveBest(res *api.QueryTableResult, traceStr *string, trace bool,
        labels []string, remoteIP net.IP, asn string, scheme string) (resolve string, repo string) {
    traceFunc := func(s string) {
        logger.Debugf(s)
        if trace {
            *traceStr += s
        }
    }

    var scores Scores
    remoteIPv4 := remoteIP.To4() != nil

    for res.Next() {
        record := res.Record()
        abbr := record.ValueByKey("mirror").(string)
        traceFunc(fmt.Sprintf("abbr: %s\n", abbr))
        endpoints, ok := AbbrToEndpoints[abbr]
        if !ok {
            continue
        }
        var scoresEndpoints Scores
        for _, endpoint := range endpoints {
            traceFunc(fmt.Sprintf("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label))
            if remoteIPv4 && !endpoint.Filter.V4 {
                traceFunc(fmt.Sprintf("    not v4 endpoint\n"))
                continue
            }
            if !remoteIPv4 && !endpoint.Filter.V6 {
                traceFunc(fmt.Sprintf("    not v6 endpoint\n"))
                continue
            }
            if scheme == "http" && !endpoint.Filter.NOSSL {
                traceFunc(fmt.Sprintf("    not nossl endpoint\n"))
                continue
            }
            if scheme == "https" && !endpoint.Filter.SSL {
                traceFunc(fmt.Sprintf("    not ssl endpoint\n"))
                continue
            }
            if (len(labels) != 0 && labels[len(labels)-1] == "4") && !endpoint.Filter.V4Only {
                traceFunc(fmt.Sprintf("    label v4only but endpoint not v4only\n"))
                continue
            }
            if (len(labels) != 0 && labels[len(labels)-1] == "6") && !endpoint.Filter.V6Only {
                traceFunc(fmt.Sprintf("    label v6only but endpoint not v6only\n"))
                continue
            }
            score := Score {pos: 0, as: 0, mask: 0, delta: 0}
            score.delta = int(record.Value().(int64))
            for index, label := range labels {
                if label == endpoint.Label {
                    score.pos = index + 1
                }
            }
            for _, endpointASN := range endpoint.RangeASN {
                if endpointASN == asn {
                    score.as = 1
                }
            }
            for _, ipnet := range endpoint.RangeCIDR {
                if remoteIP != nil && ipnet.Contains(remoteIP) {
                    mask, _ := ipnet.Mask.Size()
                    if mask > score.mask {
                        score.mask = mask
                    }
                }
            }

            score.resolve = endpoint.Resolve
            score.repo = record.ValueByKey("path").(string)
            traceFunc(fmt.Sprintf("    score: %v\n", score))

            //if score.delta < -60*60*24*3 { // 3 days
            //    traceFunc(fmt.Sprintf("    not up-to-date enough\n"))
            //    continue
            //}
            if !endpoint.Public && score.mask == 0 && score.as == 0 {
                traceFunc(fmt.Sprintf("    not hit private\n"))
                continue
            }
            scoresEndpoints = append(scoresEndpoints, score)
        }

        // Find the not-dominated scores, or the first one
        if len(scoresEndpoints) > 0 {
            optimalScores := scoresEndpoints.OptimalsExceptDelta() // Delta all the same
            if len(optimalScores) > 0 && len(optimalScores) != len(scoresEndpoints) {
                for index, score := range optimalScores {
                    traceFunc(fmt.Sprintf("  optimal scores: %d %v\n", index, score))
                    scores = append(scores, score)
                }
            } else {
                traceFunc(fmt.Sprintf("  first score: %v\n", scoresEndpoints[0]))
                scores = append(scores, scoresEndpoints[0])
            }
        } else {
            traceFunc(fmt.Sprintf("  no score found\n"))
        }
    }
    if res.Err() != nil {
        logger.Errorf("Resolve query parsing error: %s\n", res.Err().Error())
        return
    }

    var chosenScore Score
    if len(scores) > 0 {
        for index, score := range scores {
            traceFunc(fmt.Sprintf("scores: %d %v\n", index, score))
        }
        optimalScores := scores.Optimals()
        if len(optimalScores) == 0 {
            logger.Warningf("Resolve optimal scores empty, algorithm implemented error")
            chosenScore = scores[0]
        } else {
            allDelta := scores.AllDelta()
            allEqualExceptDelta := optimalScores.AllEqualExceptDelta()
            if allEqualExceptDelta || allDelta {
                var candidateScores Scores
                if (allDelta) {
                    // Note: allDelta == true implies allEqualExceptDelta == true
                    candidateScores = scores
                } else {
                    candidateScores = optimalScores
                }
                // randomly choose one mirror from the optimal half
                // when len(optimalScores) == 1, randomHalf always success
                sort.Sort(candidateScores)
                chosenScore = candidateScores.RandomHalf()
                for index, score := range candidateScores {
                    traceFunc(fmt.Sprintf("sorted delta scores: %d %v\n", index, score))
                }
            } else {
                sort.Sort(optimalScores)
                chosenScore = optimalScores[0]
                // randomly choose one mirror not dominated by others
                //chosenScore = optimalScores.Random()
                for index, score := range optimalScores {
                    traceFunc(fmt.Sprintf("optimal scores: %d %v\n", index, score))
                }
            }
        }
    }
    traceFunc(fmt.Sprintf("chosen score: %v\n", chosenScore))
    resolve = chosenScore.resolve
    repo = chosenScore.repo
    return
}

func ResolveExist(res *api.QueryTableResult, traceStr *string, trace bool,
        oldResolve string) (resolve string, repo string) {
    traceFunc := func(s string) {
        logger.Debugf(s)
        if trace {
            *traceStr += s
        }
    }

    found := false

    for res.Next() {
        record := res.Record()
        abbr := record.ValueByKey("mirror").(string)
        traceFunc(fmt.Sprintf("abbr: %s\n", abbr))
        endpoints, ok := AbbrToEndpoints[abbr]
        if !ok {
            continue
        }
        for _, endpoint := range endpoints {
            traceFunc(fmt.Sprintf("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label))

            if (oldResolve == endpoint.Resolve) {
                resolve = endpoint.Resolve
                repo = record.ValueByKey("path").(string)
                found = true
                traceFunc("exist\n")
            }
            if found {
                break
            }
        }
        if found {
            break
        }
    }
    return
}

func ResolvedInit() {
    resolved = make(map[string]Resolved)
}

func ResolvedTicker() {
    ResolvedInit()
    // GC on resolved
    ticker := time.NewTicker(time.Second * time.Duration(config.CacheTime))

    go func() {
        for {
            t := <-ticker.C
            cur := t.Unix()
            logger.Debugf("Resolved GC starts\n")
            for k, v := range resolved {
                if cur - v.start >= config.CacheTime &&
                        cur - v.last >= config.CacheTime {
                    delete(resolved, k)
                    logger.Debugf("Resolved GC %s %s\n", k, v.url)
                }
            }
            logger.Debugf("Resolved GC finished\n")
        }
    }()
}

type Endpoint struct {
    Label string `json:"label"`
    Resolve string `json:"resolve"`
    Public bool `json:"public"`
    Filter []string `json:"filter"`
    Range []string `json:"range"`
}

type EndpointInternal struct {
    Label string
    Resolve string
    Public bool
    Filter struct {
        V4 bool
        V4Only bool
        V6 bool
        V6Only bool
        SSL bool
        NOSSL bool
        SPECIAL []string
    }
    RangeASN []string
    RangeCIDR []*net.IPNet
}

type Site struct {
    Abbr string `json:"abbr"`
}

type MirrorZD struct {
    Extension string `json:"extension"`
    Endpoints []Endpoint `json:"endpoints"`
    Site Site `json:"site"`
}

func ProcessEndpoint (e Endpoint) (i EndpointInternal) {
    LabelToResolve[e.Label] = e.Resolve
    i.Label = e.Label
    i.Resolve = e.Resolve
    i.Public = e.Public
    // Filter
    for _, d := range e.Filter {
        if d == "V4" {
            i.Filter.V4 = true
        } else if d == "V6" {
            i.Filter.V6 = true
        } else if d == "NOSSL" {
            i.Filter.NOSSL = true
        } else if d == "SSL" {
            i.Filter.SSL = true
        } else {
            // TODO: more structured
            i.Filter.SPECIAL = append(i.Filter.SPECIAL, d)
        }
    }
    if i.Filter.V4 && !i.Filter.V6 {
        i.Filter.V4Only = true
    }
    if !i.Filter.V4 && i.Filter.V6 {
        i.Filter.V6Only = true
    }
    // Range
    for _, d := range e.Range {
        if strings.HasPrefix(d, "AS") {
            i.RangeASN = append(i.RangeASN, d[2:])
        } else {
            _, ipnet, _ := net.ParseCIDR(d)
            if ipnet != nil {
                i.RangeCIDR = append(i.RangeCIDR, ipnet)
            }
        }
    }
    return
}

func LoadMirrorZD (path string) (err error) {
    AbbrToEndpoints = make(map[string][]EndpointInternal)
    LabelToResolve = make(map[string]string)
    files, err := ioutil.ReadDir(path)
    if err != nil {
        logger.Errorf("LoadMirrorZD: can not open mirrorz.d directory, %v\n", err)
        return
    }
    for _, file := range files {
        if !strings.HasSuffix(file.Name(), ".json") {
            continue
        }
        content, err := ioutil.ReadFile(filepath.Join(path, file.Name()))
        if err != nil {
            logger.Errorf("LoadMirrorZD: read %s failed\n", file.Name())
            continue
        }
        var data MirrorZD
        err = json.Unmarshal([]byte(content), &data)
        if err != nil {
            logger.Errorf("LoadMirrorZD: process %s failed\n", file.Name())
            continue
        }
        logger.Infof("%+v\n", data)
        var endpointsInternal []EndpointInternal
        for _, e := range data.Endpoints {
            endpointsInternal = append(endpointsInternal, ProcessEndpoint(e))
        }
        AbbrToEndpoints[data.Site.Abbr] = endpointsInternal
    }
    for label, resolve := range LabelToResolve {
        logger.Infof("%s -> %s\n", label, resolve)
    }
    return
}

func OpenInfluxDB() {
    client = influxdb2.NewClient(config.InfluxDBURL, config.InfluxDBToken)
    queryAPI = client.QueryAPI(config.InfluxDBOrg)
}

func CloseInfluxDB() {
    client.Close()
}

func main() {
    rand.Seed(time.Now().Unix())

    configPtr := flag.String("config", "config.json", "path to config file")
    debugPtr := flag.Bool("debug", false, "debug mode")
    flag.Parse()
    LoadConfig(*configPtr, *debugPtr)

    OpenInfluxDB()

    LoadMirrorZD(config.MirrorZDDirectory)

    signalChannel := make(chan os.Signal, 1)
    signal.Notify(signalChannel, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
    go func(){
        for sig := range signalChannel {
            switch sig {
            case syscall.SIGHUP:
                logger.Infof("Got A HUP Signal! Now Reloading mirrorz.d.json....\n")
                LoadMirrorZD(config.MirrorZDDirectory)
            case syscall.SIGUSR1:
                logger.Infof("Got A USR1 Signal! Now Reloading config.json....\n")
                LoadConfig(*configPtr, *debugPtr)
            case syscall.SIGUSR2:
                logger.Infof("Got A USR2 Signal! Now Flush Resolved....\n")
                ResolvedInit()
            }
        }
    }()

    ResolvedTicker()

    http.HandleFunc("/", Handler)
    logger.Errorf("HTTP Server error: %v\n", http.ListenAndServe(config.HTTPBindAddress, nil))

    CloseInfluxDB()
}
