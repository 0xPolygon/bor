package snap

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestStorageResponseChunkBoundaries(t *testing.T) {
	for _, scheme := range []string{rawdb.HashScheme, rawdb.PathScheme} {
		t.Run(scheme, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			syncer := NewSyncer(db, scheme)
			account, root := common.Hash{1}, common.Hash{2}
			slot, value := common.Hash{2: 1}, []byte{1}
			task := &accountTask{
				SubTasks: make(map[common.Hash][]*storageTask),
				res: &accountResponse{
					hashes:   []common.Hash{account},
					accounts: []*types.StateAccount{{Root: root}},
				},
				pend:      1,
				needState: []bool{true},
				needHeal:  []bool{false},
			}
			syncer.processStorageResponse(&storageResponse{
				mainTask: task,
				accounts: []common.Hash{account},
				roots:    []common.Hash{root},
				hashes:   [][]common.Hash{{slot}},
				slots:    [][][]byte{{value}},
				cont:     true,
			})
			chunks := task.SubTasks[account]
			if len(chunks) != storageConcurrency {
				t.Fatalf("chunks = %d, want %d", len(chunks), storageConcurrency)
			}
			if chunks[0].Next != incHash(slot) || chunks[len(chunks)-1].Last != common.MaxHash {
				t.Fatal("chunks do not cover the remaining storage range")
			}
			for i, chunk := range chunks {
				if chunk.root != root || chunk.Next.Cmp(chunk.Last) > 0 || chunk.done {
					t.Fatalf("invalid chunk %d: %+v", i, chunk)
				}
				if i > 0 && chunk.Next != incHash(chunks[i-1].Last) {
					t.Fatalf("gap or overlap before chunk %d", i)
				}
			}
			if !task.needHeal[0] || !task.needState[0] || task.pend != 1 {
				t.Fatal("partial storage response incorrectly completed the account")
			}
			if got := rawdb.ReadStorageSnapshot(db, account, slot); !bytes.Equal(got, value) || syncer.storageSynced != 1 {
				t.Fatalf("persisted storage = %x, count = %d", got, syncer.storageSynced)
			}
		})
	}
}

func TestHealStorageSnapshot(t *testing.T) {
	for _, bytecodeOnly := range []bool{false, true} {
		name := "full state"
		if bytecodeOnly {
			name = "bytecode only"
		}
		t.Run(name, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			syncer := NewSyncer(db, rawdb.PathScheme)
			syncer.SetBytecodeOnlyMode(bytecodeOnly)
			account, slot, value := common.Hash{1}, common.Hash{2}, []byte{3}
			if err := syncer.onHealState([][]byte{account[:], slot[:]}, value); err != nil {
				t.Fatal(err)
			}
			if err := syncer.stateWriter.Write(); err != nil {
				t.Fatal(err)
			}
			want, count, size := value, uint64(1), common.StorageSize(1+2*common.HashLength+len(value))
			if bytecodeOnly {
				want, count, size = nil, 0, 0
			}
			if got := rawdb.ReadStorageSnapshot(db, account, slot); !bytes.Equal(got, want) {
				t.Fatalf("healed storage = %x, want %x", got, want)
			}
			if syncer.storageHealed != count || syncer.storageHealedBytes != size {
				t.Fatalf("healed slots/bytes = %d/%v, want %d/%v", syncer.storageHealed, syncer.storageHealedBytes, count, size)
			}
		})
	}
}
