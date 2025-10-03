package blockstm

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/emirpasic/gods/maps/treemap"

	"github.com/ethereum/go-ethereum/common"
)

const FlagDone = 0
const FlagEstimate = 1

const addressType = 1
const stateType = 2
const subpathType = 3

const KeyLength = common.AddressLength + common.HashLength + 2

type Key [KeyLength]byte

func (k Key) IsAddress() bool {
	return k[KeyLength-1] == addressType
}

func (k Key) IsState() bool {
	return k[KeyLength-1] == stateType
}

func (k Key) IsSubpath() bool {
	return k[KeyLength-1] == subpathType
}

func (k Key) GetAddress() common.Address {
	return common.BytesToAddress(k[:common.AddressLength])
}

func (k Key) GetStateKey() common.Hash {
	return common.BytesToHash(k[common.AddressLength : KeyLength-2])
}

func (k Key) GetSubpath() byte {
	return k[KeyLength-2]
}

func newKey(addr common.Address, hash common.Hash, subpath byte, keyType byte) Key {
	var k Key

	copy(k[:common.AddressLength], addr.Bytes())
	copy(k[common.AddressLength:KeyLength-2], hash.Bytes())
	k[KeyLength-2] = subpath
	k[KeyLength-1] = keyType

	return k
}

func NewAddressKey(addr common.Address) Key {
	return newKey(addr, common.Hash{}, 0, addressType)
}

func NewStateKey(addr common.Address, hash common.Hash) Key {
	k := newKey(addr, hash, 0, stateType)
	if !k.IsState() {
		panic(fmt.Errorf("key is not a state key"))
	}

	return k
}

func NewSubpathKey(addr common.Address, subpath byte) Key {
	return newKey(addr, common.Hash{}, subpath, subpathType)
}

type MVHashMap struct {
	m sync.Map
	s sync.Map
}

func MakeMVHashMap() *MVHashMap {
	return &MVHashMap{}
}

type WriteCell struct {
	flag        uint
	incarnation int
	data        interface{}
}

type TxnIndexCells struct {
	rw sync.RWMutex
	tm *treemap.Map
}

type Version struct {
	TxnIndex    int
	Incarnation int
}

func (mv *MVHashMap) getKeyCells(k Key, fNoKey func(kenc Key) *TxnIndexCells) (cells *TxnIndexCells) {
	val, ok := mv.m.Load(k)

	if !ok {
		cells = fNoKey(k)
	} else {
		cells = val.(*TxnIndexCells)
	}

	return
}

func (mv *MVHashMap) Write(k Key, v Version, data interface{}) {
	cells := mv.getKeyCells(k, func(kenc Key) (cells *TxnIndexCells) {
		n := &TxnIndexCells{
			rw: sync.RWMutex{},
			tm: treemap.NewWithIntComparator(),
		}
		val, _ := mv.m.LoadOrStore(kenc, n)
		cells = val.(*TxnIndexCells)

		return
	})

	cells.rw.Lock()
	if ci, ok := cells.tm.Get(v.TxnIndex); !ok {
		cells.tm.Put(v.TxnIndex, &WriteCell{
			flag:        FlagDone,
			incarnation: v.Incarnation,
			data:        data,
		})
	} else {
		if ci.(*WriteCell).incarnation > v.Incarnation {
			panic(fmt.Errorf("existing transaction value does not have lower incarnation: %v, %v",
				k, v.TxnIndex))
		}
		ci.(*WriteCell).flag = FlagDone
		ci.(*WriteCell).incarnation = v.Incarnation
		ci.(*WriteCell).data = data
	}
	cells.rw.Unlock()
}

func (mv *MVHashMap) ReadStorage(k Key, fallBack func() any) any {
	data, ok := mv.s.Load(string(k[:]))
	if !ok {
		data = fallBack()
		data, _ = mv.s.LoadOrStore(string(k[:]), data)
	}

	return data
}

func (mv *MVHashMap) MarkEstimate(k Key, txIdx int) {
	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		panic(fmt.Errorf("path must already exist"))
	})

	cells.rw.Lock()
	if ci, ok := cells.tm.Get(txIdx); !ok {
		panic(fmt.Sprintf("should not happen - cell should be present for path. TxIdx: %v, path, %x, cells keys: %v", txIdx, k, cells.tm.Keys()))
	} else {
		ci.(*WriteCell).flag = FlagEstimate
	}
	cells.rw.Unlock()
}

func (mv *MVHashMap) Delete(k Key, txIdx int) {
	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		panic(fmt.Errorf("path must already exist"))
	})

	cells.rw.Lock()
	defer cells.rw.Unlock()
	cells.tm.Remove(txIdx)
}

const (
	MVReadResultDone       = 0
	MVReadResultDependency = 1
	MVReadResultNone       = 2
)

type MVReadResult struct {
	depIdx      int
	incarnation int
	value       interface{}
}

func (res *MVReadResult) DepIdx() int {
	return res.depIdx
}

func (res *MVReadResult) Incarnation() int {
	return res.incarnation
}

func (res *MVReadResult) Value() interface{} {
	return res.value
}

func (res MVReadResult) Status() int {
	if res.depIdx != -1 {
		if res.incarnation == -1 {
			return MVReadResultDependency
		} else {
			return MVReadResultDone
		}
	}

	return MVReadResultNone
}

func (mv *MVHashMap) Read(k Key, txIdx int) (res MVReadResult) {
	res.depIdx = -1
	res.incarnation = -1

	cells := mv.getKeyCells(k, func(_ Key) *TxnIndexCells {
		return nil
	})
	if cells == nil {
		return
	}

	cells.rw.RLock()

	fk, fv := cells.tm.Floor(txIdx - 1)

	if fk != nil && fv != nil {
		c := fv.(*WriteCell)
		switch c.flag {
		case FlagEstimate:
			res.depIdx = fk.(int)
			res.value = c.data
		case FlagDone:
			{
				res.depIdx = fk.(int)
				res.incarnation = c.incarnation
				res.value = c.data
			}
		default:
			panic(fmt.Errorf("should not happen - unknown flag value"))
		}
	}

	cells.rw.RUnlock()

	return
}

func (mv *MVHashMap) FlushMVWriteSet(writes []WriteDescriptor) {
	for _, v := range writes {
		mv.Write(v.Path, v.V, v.Val)
	}
}

func ValidateVersion(txIdx int, lastInputOutput *TxnInputOutput, versionedData *MVHashMap) (valid bool) {
	valid = true

	for _, rd := range lastInputOutput.ReadSet(txIdx) {
		mvResult := versionedData.Read(rd.Path, txIdx)
		switch mvResult.Status() {
		case MVReadResultDone:
			valid = rd.Kind == ReadKindMap && rd.V == Version{
				TxnIndex:    mvResult.depIdx,
				Incarnation: mvResult.incarnation,
			}
		case MVReadResultDependency:
			valid = false
		case MVReadResultNone:
			valid = rd.Kind == ReadKindStorage // feels like an assertion?
		default:
			panic(fmt.Errorf("should not happen - undefined mv read status: %ver", mvResult.Status()))
		}

		if !valid {
			break
		}
	}

	return
}

// json models
type jsonKey struct {
	Type     string `json:"type"`               // address | state | subpath
	Address  string `json:"address"`            // 0x...
	StateKey string `json:"stateKey,omitempty"` // 0x..., only for state
	Subpath  uint8  `json:"subpath,omitempty"`  // only for subpath
}

type jsonWriteCell struct {
	TxIndex     int    `json:"txIndex"`
	Flag        string `json:"flag"`        // done | estimate
	Incarnation int    `json:"incarnation"` // -1 means unknown / unset
	Data        string `json:"data"`        // stringified
}

type jsonVersionedEntry struct {
	Key      jsonKey         `json:"key"`
	Versions []jsonWriteCell `json:"versions"` // sorted by txIndex asc
}

type jsonStorageCacheEntry struct {
	Key   jsonKey `json:"key"`
	Value string  `json:"value"`
}

type jsonMVHashMapDump struct {
	Versioned    []jsonVersionedEntry    `json:"versioned"`    // sorted by key
	StorageCache []jsonStorageCacheEntry `json:"storageCache"` // sorted by key
}

// ToJSON returns a complete JSON dump of the MVHashMap (stable order).
func (mv *MVHashMap) ToJSON() string {
	// --- collect versioned entries from mv.m ---
	type keyed struct {
		keyBytes Key
		entry    jsonVersionedEntry
	}
	var versioned []keyed

	mv.m.Range(func(k any, v any) bool {
		kenc := k.(Key)
		cells := v.(*TxnIndexCells)

		// Build entry with a read-locked snapshot of the treemap.
		entry := jsonVersionedEntry{Key: keyToJSON(kenc)}

		cells.rw.RLock()
		// gods/treemap keeps keys sorted; iterate in order
		for _, kraw := range cells.tm.Keys() {
			txIdx := kraw.(int)
			if cval, ok := cells.tm.Get(txIdx); ok {
				wc := cval.(*WriteCell)
				entry.Versions = append(entry.Versions, jsonWriteCell{
					TxIndex:     txIdx,
					Flag:        flagToString(wc.flag),
					Incarnation: wc.incarnation,
					Data:        anyToString(wc.data),
				})
			}
		}
		cells.rw.RUnlock()

		versioned = append(versioned, keyed{
			keyBytes: kenc,
			entry:    entry,
		})
		return true
	})

	// stable sort by raw key bytes
	sort.Slice(versioned, func(i, j int) bool {
		ki := versioned[i].keyBytes
		kj := versioned[j].keyBytes
		for x := 0; x < KeyLength; x++ {
			if ki[x] != kj[x] {
				return ki[x] < kj[x]
			}
		}
		return false
	})

	outVersioned := make([]jsonVersionedEntry, len(versioned))
	for i := range versioned {
		outVersioned[i] = versioned[i].entry
	}

	// --- collect storage cache entries from mv.s ---
	type skeyed struct {
		keyBytes Key
		entry    jsonStorageCacheEntry
	}
	var storage []skeyed

	mv.s.Range(func(k any, v any) bool {
		// k is string of raw Key bytes; reconstruct Key
		kstr := k.(string)
		var kenc Key
		copy(kenc[:], []byte(kstr))

		entry := jsonStorageCacheEntry{
			Key:   keyToJSON(kenc),
			Value: anyToString(v),
		}
		storage = append(storage, skeyed{keyBytes: kenc, entry: entry})
		return true
	})

	sort.Slice(storage, func(i, j int) bool {
		ki := storage[i].keyBytes
		kj := storage[j].keyBytes
		for x := 0; x < KeyLength; x++ {
			if ki[x] != kj[x] {
				return ki[x] < kj[x]
			}
		}
		return false
	})

	outStorage := make([]jsonStorageCacheEntry, len(storage))
	for i := range storage {
		outStorage[i] = storage[i].entry
	}

	// marshal
	dump := jsonMVHashMapDump{
		Versioned:    outVersioned,
		StorageCache: outStorage,
	}
	b, _ := json.Marshal(dump) // safe for logging; ignore error to avoid panics
	return string(b)
}

// String implements fmt.Stringer; it returns the same JSON as ToJSON.
func (mv *MVHashMap) String() string { return mv.ToJSON() }

// --- helpers -----------------------------------------------------------------

func keyToJSON(k Key) jsonKey {
	switch {
	case k.IsAddress():
		return jsonKey{
			Type:    "address",
			Address: k.GetAddress().Hex(),
		}
	case k.IsState():
		return jsonKey{
			Type:     "state",
			Address:  k.GetAddress().Hex(),
			StateKey: k.GetStateKey().Hex(),
		}
	case k.IsSubpath():
		return jsonKey{
			Type:    "subpath",
			Address: k.GetAddress().Hex(),
			Subpath: k.GetSubpath(),
		}
	default:
		// Fallback: treat as raw address-key
		return jsonKey{
			Type:    "unknown",
			Address: k.GetAddress().Hex(),
		}
	}
}

func flagToString(f uint) string {
	switch f {
	case FlagDone:
		return "done"
	case FlagEstimate:
		return "estimate"
	default:
		return "unknown"
	}
}

func anyToString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return "0x" + hex.EncodeToString(t)
	case common.Address:
		return t.Hex()
	case *common.Address:
		return t.Hex()
	case common.Hash:
		return t.Hex()
	case *common.Hash:
		return t.Hex()
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}
