package rawdb

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

// dbWitnessStore implements WitnessStore by delegating to the existing
// key-value database functions in accessors_state.go.
type dbWitnessStore struct {
	db ethdb.Database
}

// NewDBWitnessStore creates a WitnessStore backed by the key-value database.
func NewDBWitnessStore(db ethdb.Database) WitnessStore {
	return &dbWitnessStore{db: db}
}

func (s *dbWitnessStore) ReadWitness(hash common.Hash) []byte {
	return ReadWitness(s.db, hash)
}

func (s *dbWitnessStore) WriteWitness(hash common.Hash, witness []byte) {
	WriteWitness(s.db, hash, witness)
}

func (s *dbWitnessStore) HasWitness(hash common.Hash) bool {
	return HasWitness(s.db, hash)
}

func (s *dbWitnessStore) DeleteWitness(hash common.Hash) {
	DeleteWitness(s.db, hash)
}

func (s *dbWitnessStore) Close() error {
	return nil
}
