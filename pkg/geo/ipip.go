package geo

import (
	"sync/atomic"

	"github.com/ipipdotnet/ipdb-go"
	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
)

type CityInfo = ipdb.CityInfo

var db atomic.Pointer[ipdb.City]

var defaultCityInfo = CityInfo{
	CountryName: "中国",
	RegionName:  "北京",
	Line:        "教育网",
}

var logger = logging.GetLogger("ipip", "<root>")

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
	if result.RegionName == "中国" {
		logger.Warningf("IPIP: Noteworthy RegionName: %s, %s", ip, result.RegionName)
	}
	return result, err
}
