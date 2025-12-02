package util

import (
	"log"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
)

var (
	bridgeDB          *leveldb.DB
	bridgeDBOnce      sync.Once
	bridgeDBCloseOnce sync.Once
)

// GetBridgeDBInstance get singleton object for bridge-db
func GetBridgeDBInstance(filePath string) *leveldb.DB {
	bridgeDBOnce.Do(func() {
		var err error
		bridgeDB, err = leveldb.OpenFile(filePath, nil)
		if err != nil {
			log.Fatalln("Error in Bor Opening Database", err.Error())
		}
	})

	return bridgeDB
}

// CloseBridgeDBInstance closes bridge-db instance
func CloseBridgeDBInstance() {
	bridgeDBCloseOnce.Do(func() {
		if bridgeDB != nil {
			err := bridgeDB.Close()
			if err != nil {
				panic(err)
			}
		}
	})
}
