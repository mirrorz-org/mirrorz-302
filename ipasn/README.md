# ipasn

Convert IP to ASN

## MRT data

Get from <http://routeviews.org/bgpdata/2021.08/RIBS/> and <http://routeviews.org/route-views6/bgpdata/2021.08/RIBS/>.

## zebra-dump-parser

<https://github.com/rfc1036/zebra-dump-parser>

## Commands

### Generate Data

CentOS dependency

```
yum install bzip2 time
```

Change `$ignore_v6_routes` to 0 in `zebra-dump-parser.pl`.

Then
```
bzcat rib.20210803.1600.bz2 | time ../zebra-dump-parser/zebra-dump-parser.pl > DUMP4 2> DUMP6ERR
bzcat 6.rib.20210803.1600.bz2 | time ../zebra-dump-parser/zebra-dump-parser.pl > DUMP6 2> DUMP6ERR
cat DUMP4 DUMP6 > DUMP
```

### Bring up ipasn

After you get the DUMP, you can edit `config.json` to be

```json
{
    "asn-db": "/path/to/MRT/DUMP"
}
```

then `go run .`

### log analyse

```bash
cat mirrors.log-20210727 | cut -d ' ' -f 1,10 > ip-size.log
cat ip-size.log | awk '{x[$1]+=$2} END {for(k in x){print k,x[k]}}' | sort -rnk2 > ip-sum.log
cat > ipasn.sh <<EOF
ip=\$(echo \$1 | awk '{print \$1;}')
sum=\$(echo \$1 | awk '{print \$2;}')
echo \$(curl -s "http://localhost:8889/\$ip") \$ip \$sum
EOF
cat ip-sum.log | xargs -P $(nprocs) -I '{}' bash ipasn.sh '{}' > asn-ip-sum.log # use `watch wc -l asn-ip-sum.log ip-sum.log` to see the progress
cat asn-ip-sum.log | awk '{x[$1]+=$3} END {for(k in x){print k,x[k]}}' | sort -rnk2 > asn-sum.log
wget http://www.potaroo.net/bgp/iana/asn-ctl.txt
awk '{x=$1;$1="";a[x]=a[x]$0}END{for(x in a)print x,a[x]}' asn-sum.log asn-ctl.txt | sort -rnk2 > asn-sum-ctl.log
```
