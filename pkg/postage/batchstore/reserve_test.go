// Copyright 2021 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package batchstore_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"math/big"
	"math/rand"
	"os"
	"testing"

	"github.com/ethersphere/bee/pkg/postage"
	"github.com/ethersphere/bee/pkg/postage/batchstore"
	postagetest "github.com/ethersphere/bee/pkg/postage/testing"
	"github.com/ethersphere/bee/pkg/statestore/leveldb"
	"github.com/ethersphere/bee/pkg/storage"
	"github.com/ethersphere/bee/pkg/swarm"
)

// random advance on the blockchain
func newBlockAdvance() uint64 {
	return uint64(rand.Intn(3) + 1)
}

// initial depth of a new batch
func newBatchDepth(depth uint8) uint8 {
	return depth + uint8(rand.Intn(10)) + 4
}

// the factor to increase the batch depth with
func newDilutionFactor() int {
	return rand.Intn(3) + 1
}

// new value on top of value based on random period and price
func newValue(price, value *big.Int) *big.Int {
	period := rand.Intn(100) + 1000
	v := new(big.Int).Mul(price, big.NewInt(int64(period)))
	return v.Add(v, value)
}

// TestBatchStoreUnreserve is testing the correct behaviour of the reserve.
// the following assumptions are tested on each modification of the batches (top up, depth increase, price change)
// - reserve exceeds capacity
// - value-consistency of unreserved POs
func TestBatchStoreUnreserveEvents(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(16)

	bStore, unreserved := setupBatchStore(t)
	batches := make(map[string]*postage.Batch)

	t.Run("new batches only", func(t *testing.T) {
		// iterate starting from batchstore.DefaultDepth to maxPO
		_, radius := batchstore.GetReserve(bStore)
		for step := 0; radius < swarm.MaxPO; step++ {
			cs, err := nextChainState(bStore)
			if err != nil {
				t.Fatal(err)
			}
			var b *postage.Batch
			if b, err = createBatch(bStore, cs, radius); err != nil {
				t.Fatal(err)
			}
			batches[string(b.ID)] = b
			if radius, err = checkReserve(bStore, unreserved); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("top up batches", func(t *testing.T) {
		n := 0
		for id := range batches {
			b, err := bStore.Get([]byte(id))
			if err != nil {
				if errors.Is(storage.ErrNotFound, err) {
					continue
				}
				t.Fatal(err)
			}
			cs, err := nextChainState(bStore)
			if err != nil {
				t.Fatal(err)
			}
			if err = topUp(bStore, cs, b); err != nil {
				t.Fatal(err)
			}
			if _, err = checkReserve(bStore, unreserved); err != nil {
				t.Fatal(err)
			}
			n++
			if n > len(batches)/5 {
				break
			}
		}
	})
	t.Run("dilute batches", func(t *testing.T) {
		n := 0
		for id := range batches {
			b, err := bStore.Get([]byte(id))
			if err != nil {
				if errors.Is(storage.ErrNotFound, err) {
					continue
				}
				t.Fatal(err)
			}
			cs, err := nextChainState(bStore)
			if err != nil {
				t.Fatal(err)
			}
			if err = increaseDepth(bStore, cs, b); err != nil {
				t.Fatal(err)
			}
			if _, err = checkReserve(bStore, unreserved); err != nil {
				t.Fatal(err)
			}
			n++
			if n > len(batches)/5 {
				break
			}
		}
	})
}

func TestBatchStoreUnreserveAll(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(16)

	bStore, unreserved := setupBatchStore(t)
	var batches [][]byte
	// iterate starting from batchstore.DefaultDepth to maxPO
	_, depth := batchstore.GetReserve(bStore)
	for step := 0; depth < swarm.MaxPO; step++ {
		cs, err := nextChainState(bStore)
		if err != nil {
			t.Fatal(err)
		}
		event := rand.Intn(6)
		//  0:  dilute, 1: topup, 2,3,4,5: create
		var b *postage.Batch
		if event < 2 && len(batches) > 10 {
			for {
				n := rand.Intn(len(batches))
				b, err = bStore.Get(batches[n])
				if err != nil {
					if errors.Is(storage.ErrNotFound, err) {
						continue
					}
					t.Fatal(err)
				}
				break
			}
			if event == 0 {
				if err = increaseDepth(bStore, cs, b); err != nil {
					t.Fatal(err)
				}
			} else if err = topUp(bStore, cs, b); err != nil {
				t.Fatal(err)
			}
		} else if b, err = createBatch(bStore, cs, depth); err != nil {
			t.Fatal(err)
		} else {
			batches = append(batches, b.ID)
		}
		if depth, err = checkReserve(bStore, unreserved); err != nil {
			t.Fatal(err)
		}
	}
}

func setupBatchStore(t *testing.T) (postage.Storer, map[string]uint8) {
	t.Helper()
	// we cannot  use the mock statestore here since the iterator is not giving the right order
	// must use the leveldb statestore
	dir, err := ioutil.TempDir("", "batchstore_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	})

	stateStore, err := leveldb.NewStateStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stateStore.Close(); err != nil {
			t.Fatal(err)
		}
	})

	// set mock unreserve call
	unreserved := make(map[string]uint8)
	unreserveFunc := func(batchID []byte, radius uint8) error {
		unreserved[hex.EncodeToString(batchID)] = radius
		return nil
	}
	bStore, _ := batchstore.New(stateStore, unreserveFunc)

	// initialise chainstate
	err = bStore.PutChainState(&postage.ChainState{
		Block: 666,
		Total: big.NewInt(0),
		Price: big.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bStore, unreserved
}

func nextChainState(bStore postage.Storer) (*postage.ChainState, error) {
	cs := bStore.GetChainState()
	// random advance on the blockchain
	advance := newBlockAdvance()
	cs = &postage.ChainState{
		Block: advance + cs.Block,
		Price: cs.Price,
		// settle although no price change
		Total: cs.Total.Add(cs.Total, new(big.Int).Mul(cs.Price, big.NewInt(int64(advance)))),
	}
	return cs, bStore.PutChainState(cs)
}

// creates a test batch with random value and depth and adds it to the batchstore
func createBatch(bStore postage.Storer, cs *postage.ChainState, depth uint8) (*postage.Batch, error) {
	b := postagetest.MustNewBatch()
	b.Depth = newBatchDepth(depth)
	value := newValue(cs.Price, cs.Total)
	b.Value = big.NewInt(0)
	return b, bStore.Put(b, value, b.Depth)
}

// tops up a batch with random amount
func topUp(bStore postage.Storer, cs *postage.ChainState, b *postage.Batch) error {
	value := newValue(cs.Price, b.Value)
	return bStore.Put(b, value, b.Depth)
}

// dilutes the batch with random factor
func increaseDepth(bStore postage.Storer, cs *postage.ChainState, b *postage.Batch) error {
	diff := newDilutionFactor()
	value := new(big.Int).Sub(b.Value, cs.Total)
	value.Div(value, big.NewInt(int64(1<<diff)))
	value.Add(value, cs.Total)
	return bStore.Put(b, value, b.Depth+uint8(diff))
}

// checkReserve is testing the correct behaviour of the reserve.
// the following assumptions are tested on each modification of the batches (top up, depth increase, price change)
// - reserve exceeds capacity
// - value-consistency of unreserved POs
func checkReserve(bStore postage.Storer, unreserved map[string]uint8) (uint8, error) {
	var size int64
	count := 0
	outer := big.NewInt(0)
	inner := big.NewInt(0)
	limit, depth := batchstore.GetReserve(bStore)
	// checking all batches
	err := batchstore.IterateAll(bStore, func(b *postage.Batch) (bool, error) {
		count++
		bDepth, found := unreserved[string(b.ID)]
		if !found {
			return true, fmt.Errorf("batch not unreserved")
		}
		if b.Value.Cmp(limit) >= 0 {
			if bDepth < depth-1 || bDepth > depth {
				return true, fmt.Errorf("incorrect reserve radius. expected %d or %d. got  %d", depth-1, depth, bDepth)
			}
			if bDepth == depth {
				if inner.Cmp(b.Value) < 0 {
					inner.Set(b.Value)
				}
			} else if outer.Cmp(b.Value) > 0 || outer.Cmp(big.NewInt(0)) == 0 {
				outer.Set(b.Value)
			}
			if outer.Cmp(big.NewInt(0)) != 0 && outer.Cmp(inner) <= 0 {
				return true, fmt.Errorf("inconsistent reserve radius: %d <= %d", outer.Uint64(), inner.Uint64())
			}
			size += batchstore.Exp2(b.Depth - bDepth - 1)
		} else if bDepth != swarm.MaxPO {
			return true, fmt.Errorf("batch below limit expected to be fully unreserved. got found=%v, radius=%d", found, bDepth)
		}
		return false, nil
	})
	if err != nil {
		return 0, err
	}
	if size > batchstore.Capacity {
		return 0, fmt.Errorf("reserve size beyond capacity. max %d, got %d", batchstore.Capacity, size)
	}
	return depth, nil
}
func TestBatchStore_AllBatchesInReserve(t *testing.T) {
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)

	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
	}

	b := postagetest.MustNewBatch()
	t.Logf("lastbatch: %v", hex.EncodeToString(b.ID))
	val := big.NewInt(int64(2))
	b.Value = big.NewInt(0)
	b.Depth = uint8(0)
	b.Start = 667
	batches = append(batches, b)
	_ = bStore.Put(b, val, depth)

	b = postagetest.MustNewBatch()
	t.Logf("lastbatch2: %v", hex.EncodeToString(b.ID))
	val = big.NewInt(int64(7))
	b.Value = big.NewInt(0)
	b.Depth = uint8(0)
	b.Start = 667
	batches = append(batches, b)
	_ = bStore.Put(b, val, depth)

	t.Fatalf("%v", unreserved)
	//batchstore.IterateAll(bStore, func(b *postage.Batch) (bool, error) {

	//})

}

func TestBatchStore_InnerValue1(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		t.Logf("batch %s, val %s", hex.EncodeToString(b.ID), val)
		_ = bStore.Put(b, val, depth)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// add one batch with value 2 and expect that it will be called in
	// evict with radius 5, which means that the outer half of chunks from
	// that batch will be deleted once chunks start entering the localstore.
	// inner 2, outer 4

	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(2))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, depth)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	// TODO add check for last created batch called in unreserve map
	// with radius 5, and also the batch created with value 3 before, so the last batch only incurs 4 chunks
	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue2(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// add one batch with value 3 and expect that it will be called in
	// evict with radius 5 alongside with the other value 3 batch
	// inner 3, outer 4

	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(3))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, depth)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)

}

func TestBatchStore_InnerValue3(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// add one batch with value 4 and expect that the batch with value
	// 3 gets called with radius 5, and BOTH batches with value 4 will
	// also be called with radius 5.
	// inner 3, outer 5

	b1 := postagetest.MustNewBatch()
	t.Logf("lastbatch: %v", hex.EncodeToString(b1.ID))
	val := big.NewInt(int64(4))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, depth)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)

}

func TestBatchStore_InnerValue4(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// add one batch with value 4 and expect that the batch with value
	// 3 gets called with radius 5, and BOTH batches with value 4 will
	// also be called with radius 5.
	// inner 3, outer 5

	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(4))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, depth)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	// since we over-evicted one batch before (since both 4's ended up in
	// inner, then we can add another one at 4, and expect it also to be
	// at inner (called with 5)
	// inner 3, outer 5 (stays the same)

	b2 := postagetest.MustNewBatch()
	val = big.NewInt(int64(4))
	b2.Value = big.NewInt(0)
	b2.Depth = uint8(0)
	b2.Start = 667
	batches = append(batches, b2)
	_ = bStore.Put(b2, val, depth)
	t.Logf("batch2 %x, val %s", b2.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue5(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 6 (2 chunks fall under our radius)
	// value is 3, expect unreserve 5, expect other value 3 to be
	// at radius 5.

	d2 := uint8(6)
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(3))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	b1 = postagetest.MustNewBatch()
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch2 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue6(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 6 (2 chunks fall under our radius)
	// value is 3, expect unreserve 5, expect other value 3 to be
	// at radius 5.
	// inner 3, outer 4

	d2 := uint8(6)
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(4))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue7(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 6 (2 chunks fall under our radius)
	// value is 3, expect unreserve 5, expect other value 3 to be
	// at radius 5.
	// inner 3, outer 4

	d2 := uint8(6)
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(6))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue8(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 9 (16 chunks in outer tier)
	// expect batches with value 3 and 4 to be unreserved with radius 5
	// inner 3, outer 5

	d2 := uint8(9) // 16 chunks in premium
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(3))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue9(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 9 (16 chunks in outer tier)
	// expect batches with value 3 and 4 to be unreserved with radius 5
	// inner 3, outer 5

	d2 := uint8(9) // 16 chunks in premium
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(4))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue10(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 9 (16 chunks in outer tier)
	// expect batches with value 3 and 4 to be unreserved with radius 5
	// inner 3, outer 6

	d2 := uint8(9) // 16 chunks in premium
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(5))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	d2 = uint8(7) // 8 chunks in premium
	b1 = postagetest.MustNewBatch()
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch2 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue11(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 9 (16 chunks in outer tier)
	// expect batches with value 3 and 4 to be unreserved with radius 5
	// inner 3, outer 6

	d2 := uint8(10) // 32 chunks in premium
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(3))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	//d2 = uint8(7) // 8 chunks in premium
	//b1 = postagetest.MustNewBatch()
	//b1.Value = big.NewInt(0)
	//b1.Depth = uint8(0)
	//b1.Start = 667
	//batches = append(batches, b1)
	//_ = bStore.Put(b1, val, d2)
	//t.Logf("batch2 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_InnerValue12(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 1; i <= 4; i++ {
		// values are 3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// at this stage all inserted batches fall inside the Outer part of the
	// reserve

	//TODO check all unreserve calls are @ 4

	// insert a batch of depth 9 (16 chunks in outer tier)
	// expect batches with value 3 and 4 to be unreserved with radius 5
	// inner 3, outer 6

	d2 := uint8(10) // 32 chunks in premium
	b1 := postagetest.MustNewBatch()
	val := big.NewInt(int64(6))
	b1.Value = big.NewInt(0)
	b1.Depth = uint8(0)
	b1.Start = 667
	batches = append(batches, b1)
	_ = bStore.Put(b1, val, d2)
	t.Logf("batch1 %x, val %s", b1.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_Topup1(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 0; i <= 4; i++ {
		// values are 2,3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}
	t.Logf("1st state: \n%v", unreserved)

	// topup of batch with value 2 to value 3 should result
	// in the same state as before
	// inner 3, outer 4
	val := big.NewInt(int64(3))

	_ = bStore.Put(batches[0], val, depth)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_Topup2(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 0; i <= 4; i++ {
		// values are 2,3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// topup of batch with value 2 to value 4 should result
	// in the other batches (3,4) in being downgraded to inner too, so all three batches are
	// at inner. there's excess capacity
	// inner 3, outer 5
	val := big.NewInt(int64(4))

	_ = bStore.Put(batches[0], val, depth)
	t.Logf("topup:\n%v", unreserved)

	// add another batch at value 2, and since we've over-evicted before,
	// we should be able to accommodate it
	b := postagetest.MustNewBatch()
	val = big.NewInt(int64(2))
	b.Value = big.NewInt(0)
	b.Depth = uint8(0)
	b.Start = 667
	batches = append(batches, b)
	_ = bStore.Put(b, val, depth)
	t.Logf("new batch %x, val %s", b.ID, val)

	t.Fatalf("%v", unreserved)
}

func TestBatchStore_Topup3(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 0; i <= 4; i++ {
		// values are 2,3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// topup of batch with value 2 to value 4 should result
	// in the other batches (3,4) in being downgraded to inner too, so all three batches are
	// at inner. there's excess capacity
	// inner 3, outer 5
	val := big.NewInt(int64(10))

	_ = bStore.Put(batches[3], val, depth)
	t.Logf("topup:\n%v", unreserved)

	// add another batch at value 2, and since we've over-evicted before,
	// we should be able to accommodate it
	b, err := bStore.Get(batches[3].ID)
	if err != nil {
		t.Fatal(err)
	}

	// TODO: check value in the actual test
	t.Fatalf("%s", b.Value)
}

func TestBatchStore_Dilution(t *testing.T) {
	// temporarily reset reserve Capacity
	defer func(i int64) {
		batchstore.Capacity = i
	}(batchstore.Capacity)
	batchstore.Capacity = batchstore.Exp2(5) // 32 chunks

	bStore, unreserved := setupBatchStore(t)

	// batch depth 8 means 8 chunks falling in a neighborhood
	// assuming constant bucket depth
	depth := uint8(8)

	// create the initial state
	var batches []*postage.Batch
	for i := 0; i <= 4; i++ {
		// values are 2,3,4,5,6
		b := postagetest.MustNewBatch()
		val := big.NewInt(int64(i + 2))
		b.Value = big.NewInt(0)
		b.Depth = uint8(0)
		b.Start = 667
		batches = append(batches, b)
		_ = bStore.Put(b, val, depth)
		t.Logf("batch %x, val %s", b.ID, val)
	}

	// dilution halves the value
	t.Logf("%v", unreserved)

	// double the size of the batch
	// recalculate the per chunk balance. ((value - total) / 2) + total => new batch value
	// total is 0 at this point
	t.Fatal(bStore.GetChainState().Total)
	val := big.NewInt(int64(3))
	d2 := uint8(9) // if d2 would be 10, then val becomes 1
	_ = bStore.Put(batches[5], val, d2)
	t.Fatal(unreserved)
}

/*
- add a batch, and expect the inner value to be set to that batch's value
- when we reach the capacity limit, depth 4, expect 32 chunks in the reserve, we add
  4 batches of 8, values are of 4,3,2,1. ordering should not matter. put it in the same block height
	sub test:
	 - what happens when you add a batch of size 2, prices vary - expect cheapest 8 size batch to be unreserved
	 outer value should be 1+1.
		- 8+8+8+8, inner: lowest(1), outer: same. batch will value 0 expect nothing to happen
		  add a batch with value 1, nothing happens
			size 2, value 2: outer half of batch with value 1 should be evicted, depth increases, unreserve called with new batch
			size 8 value 1: nothing happens
			size 4 value 2: delete half the chunks of price 1
			size 8 value 2: delete all chunks of price 1 and outer half of the
			size 16 value 3:


*/
