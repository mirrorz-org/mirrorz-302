package geo

import "math"

type GeoInfo struct {
	Name                string
	Latitude, Longitude float64
}

var codeToInfo = map[string]GeoInfo{
	"BJ": {"北京", 39.90403, 116.40753},
	"TJ": {"天津", 39.1467, 117.2056},
	"HE": {"河北", 38.0425, 114.51},
	"SX": {"山西", 37.8704, 112.5497},
	"NM": {"内蒙古", 40.842, 111.749},
	"LN": {"辽宁", 41.8025, 123.428056},
	"JL": {"吉林", 43.897, 125.326},
	"HL": {"黑龙江", 45.7576, 126.6409},
	"SH": {"上海", 31.228611, 121.474722},
	"JS": {"江苏", 32.060833, 118.778889},
	"ZJ": {"浙江", 30.267, 120.153},
	"AH": {"安徽", 31.8206, 117.2273},
	"FJ": {"福建", 26.0743, 119.2964},
	"JX": {"江西", 28.683, 115.858},
	"SD": {"山东", 36.6702, 117.0207},
	"HA": {"河南", 34.764, 113.684},
	"HB": {"湖北", 30.5934, 114.3046},
	"HN": {"湖南", 28.228, 112.939},
	"GD": {"广东", 23.13, 113.26},
	"GX": {"广西", 22.8167, 108.3275},
	"HI": {"海南", 20.0186, 110.3488},
	"CQ": {"重庆", 29.5637, 106.5504},
	"SC": {"四川", 30.66, 104.063333},
	"GZ": {"贵州", 26.647, 106.63},
	"YN": {"云南", 25.0464, 102.7094},
	"XZ": {"西藏", 29.6487, 91.1174},
	"SN": {"陕西", 34.265, 108.954},
	"GS": {"甘肃", 36.0606, 103.8268},
	"QH": {"青海", 36.6224, 101.7804},
	"NX": {"宁夏", 38.472, 106.2589},
	"XJ": {"新疆", 43.8225, 87.6125},
	"TW": {"台湾", 25.0375, 121.5625},
	"HK": {"香港", 22.3, 114.2},
	"MO": {"澳门", 22.166667, 113.55},
}

var nameToCode = make(map[string]string, len(codeToInfo))

var ispCodeToName = map[string]string{
	"CERNET":   "教育网",
	"CMCC":     "移动",
	"CHINANET": "电信",
	"UNICOM":   "联通",
	"CSTNET":   "科技网",
	"DRPENG":   "鹏博士",
	"CBNET":    "广电网",
}

var ispNameToCode = make(map[string]string, len(ispCodeToName))

func init() {
	for k, v := range codeToInfo {
		nameToCode[v.Name] = k
	}
	for k, v := range ispCodeToName {
		ispNameToCode[v] = k
	}
}

// GetGeoInfo returns the GeoInfo of a given code.
func GetGeoInfo(code string) (GeoInfo, bool) {
	info, ok := codeToInfo[code]
	return info, ok
}

// NameToCode looks up the code of a given location name.
// If the name is not found, an empty string is returned.
func NameToCode(name string) string {
	return nameToCode[name]
}

// ISPNameToCode looks up the code of a given ISP name.
// If the name is not found, an empty string is returned.
func ISPNameToCode(name string) string {
	return ispNameToCode[name]
}

// The radius of the Earth in metres.
const EarthRadius = 6378.1e3

// Calculate the great circle distance between two points on the Earth,
// given their latitude and longitude in degrees.
//
// This is the Haversine formula implemented following https://www.movable-type.co.uk/scripts/latlong.html
func Haversine(lat1, long1, lat2, long2 float64) float64 {
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	dPhi := phi2 - phi1
	dLambda := (long2 - long1) * math.Pi / 180
	a := math.Sin(dPhi/2)*math.Sin(dPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*
			math.Sin(dLambda/2)*math.Sin(dLambda/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return EarthRadius * c
}

// GeoDistance returns the distance between two geolocations in metres.
func GeoDistance(code1, code2 string) float64 {
	info1, ok1 := GetGeoInfo(code1)
	info2, ok2 := GetGeoInfo(code2)
	if !ok1 || !ok2 {
		return math.Inf(1)
	}
	return Haversine(info1.Latitude, info1.Longitude, info2.Latitude, info2.Longitude)
}
