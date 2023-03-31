package geo

import (
	"sync/atomic"

	"github.com/ipipdotnet/ipdb-go"
	"github.com/juju/loggo"
)

type CityInfo = ipdb.CityInfo

var db atomic.Pointer[ipdb.City]

var defaultCityInfo = CityInfo{
	CountryName: "中国",
	RegionName:  "北京",
	Line:        "教育网",
}

var logger = loggo.GetLogger("ipip")

func LoadIPDB(filename string) error {
	newdb, err := ipdb.NewCity(filename)
	if err != nil {
		return err
	}
	db.Store(newdb)
	return nil
}

func Lookup(ip string) (*CityInfo, error) {
	p := db.Load()
	if p == nil {
		// No database loaded, return a copy of the placeholder data.
		ci := defaultCityInfo
		return &ci, nil
	}
	result, err := p.FindInfo(ip, "CN")
	if result.CountryName == result.RegionName {
		logger.Warningf("IPIP: Identical CountryName and RegionName: %s, %s", ip, result.CountryName)
	}
	return result, err
}
