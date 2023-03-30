package geo

import (
	"sync/atomic"

	"github.com/ipipdotnet/ipdb-go"
)

type CityInfo = ipdb.CityInfo

var (
	db atomic.Pointer[ipdb.City]
)

func Load(filename string) error {
	newdb, err := ipdb.NewCity(filename)
	if err != nil {
		return err
	}
	db.Store(newdb)
	return nil
}

func Lookup(ip string) (*CityInfo, error) {
	return db.Load().FindInfo(ip, "CN")
}
