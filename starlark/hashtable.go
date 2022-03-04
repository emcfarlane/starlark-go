// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package starlark

import (
	"fmt"
	_ "unsafe" // for go:linkname hack
)

// hashtable is used to represent Starlark dict and set values.
// It is a hash table whose key/value entries form a doubly-linked list
// in the order the entries were inserted.
type hashtable struct {
	table     []bucket  // len is zero or a power of two
	bucket0   [1]bucket // inline allocation for small maps.
	len       uint32
	itercount uint32  // number of active iterators (ignored if frozen)
	head      *entry  // insertion order doubly-linked list; may be nil
	tailLink  **entry // address of nil link at end of list (perhaps &head)
	frozen    bool
}

const bucketSize = 8

type bucket struct {
	entries [bucketSize]entry
	next    *bucket // linked list of buckets
}

type entry struct {
	hash       uint32 // nonzero => in use
	key, value Value
	next       *entry  // insertion order doubly-linked list; may be nil
	prevLink   **entry // address of link to this entry (perhaps &head)
}

func (ht *hashtable) init(size int) {
	if size < 0 {
		panic("size < 0")
	}
	nb := 1
	for overloaded(size, nb) {
		nb = nb << 1
	}
	if nb < 2 {
		ht.table = ht.bucket0[:1]
	} else {
		ht.table = make([]bucket, nb)
	}
	ht.tailLink = &ht.head
}

func (ht *hashtable) freeze() {
	if !ht.frozen {
		ht.frozen = true
		for e := ht.head; e != nil; e = e.next {
			e.key.Freeze()
			e.value.Freeze()
		}
	}
}

func (ht *hashtable) insert(k, v Value) error {
	if ht.frozen {
		return fmt.Errorf("cannot insert into frozen hash table")
	}
	if ht.itercount > 0 {
		return fmt.Errorf("cannot insert into hash table during iteration")
	}
	if ht.table == nil {
		ht.init(1)
	}
	h, err := k.Hash()
	if err != nil {
		return err
	}
	if h == 0 {
		h = 1 // zero is reserved
	}

retry:
	var insert *entry

	// Inspect each bucket in the bucket list.
	p := &ht.table[h&(uint32(len(ht.table)-1))]
	for {
		for i := range p.entries {
			e := &p.entries[i]
			if e.hash != h {
				if e.hash == 0 {
					// Found empty entry; make a note.
					insert = e
				}
				continue
			}
			if eq, err := Equal(k, e.key); err != nil {
				return err // e.g. excessively recursive tuple
			} else if !eq {
				continue
			}
			// Key already present; update value.
			e.value = v
			return nil
		}
		if p.next == nil {
			break
		}
		p = p.next
	}

	// Key not found.  p points to the last bucket.

	// Does the number of elements exceed the buckets' load factor?
	if overloaded(int(ht.len), len(ht.table)) {
		ht.grow()
		goto retry
	}

	if insert == nil {
		// No space in existing buckets. Add a new one to the bucket list.
		b := new(bucket)
		p.next = b
		insert = &b.entries[0]
	}

	// Insert key/value pair.
	insert.hash = h
	insert.key = k
	insert.value = v

	// Append entry to doubly-linked list.
	insert.prevLink = ht.tailLink
	*ht.tailLink = insert
	ht.tailLink = &insert.next

	ht.len++

	return nil
}

func overloaded(elems, buckets int) bool {
	const loadFactor = 6.5 // just a guess
	return elems >= bucketSize && float64(elems) >= loadFactor*float64(buckets)
}

func (ht *hashtable) grow() {
	// Double the number of buckets and rehash.
	// TODO(adonovan): opt:
	// - avoid reentrant calls to ht.insert, and specialize it.
	//   e.g. we know the calls to Equals will return false since
	//   there are no duplicates among the old keys.
	// - saving the entire hash in the bucket would avoid the need to
	//   recompute the hash.
	// - save the old buckets on a free list.
	ht.table = make([]bucket, len(ht.table)<<1)
	oldhead := ht.head
	ht.head = nil
	ht.tailLink = &ht.head
	ht.len = 0
	for e := oldhead; e != nil; e = e.next {
		ht.insert(e.key, e.value)
	}
	ht.bucket0[0] = bucket{} // clear out unused initial bucket
}

func (ht *hashtable) lookup(k Value) (v Value, found bool, err error) {
	h, err := k.Hash()
	if err != nil {
		return nil, false, err // unhashable
	}
	if h == 0 {
		h = 1 // zero is reserved
	}
	if ht.table == nil {
		return None, false, nil // empty
	}

	// Inspect each bucket in the bucket list.
	for p := &ht.table[h&(uint32(len(ht.table)-1))]; p != nil; p = p.next {
		for i := range p.entries {
			e := &p.entries[i]
			if e.hash == h {
				if eq, err := Equal(k, e.key); err != nil {
					return nil, false, err // e.g. excessively recursive tuple
				} else if eq {
					return e.value, true, nil // found
				}
			}
		}
	}
	return None, false, nil // not found
}

// Items returns all the items in the map (as key/value pairs) in insertion order.
func (ht *hashtable) items() []Tuple {
	items := make([]Tuple, 0, ht.len)
	array := make([]Value, ht.len*2) // allocate a single backing array
	for e := ht.head; e != nil; e = e.next {
		pair := Tuple(array[:2:2])
		array = array[2:]
		pair[0] = e.key
		pair[1] = e.value
		items = append(items, pair)
	}
	return items
}

func (ht *hashtable) first() (Value, bool) {
	if ht.head != nil {
		return ht.head.key, true
	}
	return None, false
}

func (ht *hashtable) keys() []Value {
	keys := make([]Value, 0, ht.len)
	for e := ht.head; e != nil; e = e.next {
		keys = append(keys, e.key)
	}
	return keys
}

func (ht *hashtable) delete(k Value) (v Value, found bool, err error) {
	if ht.frozen {
		return nil, false, fmt.Errorf("cannot delete from frozen hash table")
	}
	if ht.itercount > 0 {
		return nil, false, fmt.Errorf("cannot delete from hash table during iteration")
	}
	if ht.table == nil {
		return None, false, nil // empty
	}
	h, err := k.Hash()
	if err != nil {
		return nil, false, err // unhashable
	}
	if h == 0 {
		h = 1 // zero is reserved
	}

	// Inspect each bucket in the bucket list.
	for p := &ht.table[h&(uint32(len(ht.table)-1))]; p != nil; p = p.next {
		for i := range p.entries {
			e := &p.entries[i]
			if e.hash == h {
				if eq, err := Equal(k, e.key); err != nil {
					return nil, false, err
				} else if eq {
					// Remove e from doubly-linked list.
					*e.prevLink = e.next
					if e.next == nil {
						ht.tailLink = e.prevLink // deletion of last entry
					} else {
						e.next.prevLink = e.prevLink
					}

					v := e.value
					*e = entry{}
					ht.len--
					return v, true, nil // found
				}
			}
		}
	}

	// TODO(adonovan): opt: remove completely empty bucket from bucket list.

	return None, false, nil // not found
}

func (ht *hashtable) clear() error {
	if ht.frozen {
		return fmt.Errorf("cannot clear frozen hash table")
	}
	if ht.itercount > 0 {
		return fmt.Errorf("cannot clear hash table during iteration")
	}
	if ht.table != nil {
		for i := range ht.table {
			ht.table[i] = bucket{}
		}
	}
	ht.head = nil
	ht.tailLink = &ht.head
	ht.len = 0
	return nil
}

// dump is provided as an aid to debugging.
func (ht *hashtable) dump() {
	fmt.Printf("hashtable %p len=%d head=%p tailLink=%p",
		ht, ht.len, ht.head, ht.tailLink)
	if ht.tailLink != nil {
		fmt.Printf(" *tailLink=%p", *ht.tailLink)
	}
	fmt.Println()
	for j := range ht.table {
		fmt.Printf("bucket chain %d\n", j)
		for p := &ht.table[j]; p != nil; p = p.next {
			fmt.Printf("bucket %p\n", p)
			for i := range p.entries {
				e := &p.entries[i]
				fmt.Printf("\tentry %d @ %p hash=%d key=%v value=%v\n",
					i, e, e.hash, e.key, e.value)
				fmt.Printf("\t\tnext=%p &next=%p prev=%p",
					e.next, &e.next, e.prevLink)
				if e.prevLink != nil {
					fmt.Printf(" *prev=%p", *e.prevLink)
				}
				fmt.Println()
			}
		}
	}
}

func (ht *hashtable) iterate() *keyIterator {
	if !ht.frozen {
		ht.itercount++
	}
	return &keyIterator{ht: ht, e: ht.head}
}

type keyIterator struct {
	ht *hashtable
	e  *entry
}

func (it *keyIterator) Next(k *Value) bool {
	if it.e != nil {
		*k = it.e.key
		it.e = it.e.next
		return true
	}
	return false
}

func (it *keyIterator) Done() {
	if !it.ht.frozen {
		it.ht.itercount--
	}
}

// hashString computes the hash of s.
func hashString(s string) uint32 {
	if len(s) >= 12 {
		// Call the Go runtime's optimized hash implementation,
		// which uses the AESENC instruction on amd64 machines.
		return uint32(goStringHash(s, 0))
	}
	return softHashString(s)
}

//go:linkname goStringHash runtime.stringHash
func goStringHash(s string, seed uintptr) uintptr

// softHashString computes the 32-bit FNV-1a hash of s in software.
func softHashString(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// An OrderedStringDict is a mapping from names to values with support
// for fast indexing and range operations. Once created keys cannot be
// added or removed but values can be mutated.
// It is not a true starlark.Value.
type OrderedStringDict struct {
	// Implementation based on the above hashtable.
	table   []osdBucket  // len is zero or a power of two
	bucket0 [1]osdBucket // inline allocation for small maps.
	entries []osdEntry   // sorted list of entries
}

type osdBucket struct {
	entries [bucketSize]*osdEntry
	next    *osdBucket // linked list of buckets
}

type osdEntry struct {
	hash  uint32 // nonzero => in use
	key   string
	value Value
}

func (d *OrderedStringDict) init(size int) {
	if size < 0 {
		panic("size < 0")
	}
	nb := 1
	for overloaded(size, nb) {
		nb = nb << 1
	}
	if nb < 2 {
		d.table = d.bucket0[:1]
	} else {
		d.table = make([]osdBucket, nb)
	}
	d.entries = make([]osdEntry, 0, size)
}

func (d *OrderedStringDict) getEntry(h uint32, k string) *osdEntry {
	if d.table == nil {
		return nil // empty
	}

	// Inspect each bucket in the bucket list.
	for p := &d.table[h&(uint32(len(d.table)-1))]; p != nil; p = p.next {
		for _, e := range p.entries {
			if e == nil {
				break
			}
			if e.hash == h && k == e.key {
				return e // found
			}
		}
	}
	return nil // not found
}

func (d *OrderedStringDict) grow() {
	// Double the number of buckets and rehash.
	d.table = make([]osdBucket, len(d.table)<<1)
	oldEntries := d.entries
	d.entries = make([]osdEntry, 0, len(d.entries)<<1)
	for _, e := range oldEntries {
		d.append(e.hash, e.key, e.value) // can't error
	}
	d.bucket0[0] = osdBucket{} // clear out unused initial bucket
}

func (d *OrderedStringDict) append(h uint32, k string, v Value) error {
	if d.table == nil {
		d.init(1)
	}

	// Does the number of elements exceed the buckets' load factor?
	for overloaded(len(d.entries), len(d.table)) {
		d.grow()
	}

	// Find the bucket position for the new entry.
	position := -1

	p := &d.table[h&(uint32(len(d.table)-1))]
	for {
		for i, e := range p.entries {
			if e == nil {
				position = i
				break
			}
			if k == e.key {
				return fmt.Errorf("duplicate key %s", k)
			}
		}
		if p.next == nil || position != -1 {
			break
		}
		p = p.next
	}

	if position == -1 {
		// No space in existing buckets. Add a new one to the bucket list.
		b := new(osdBucket)
		p.next = b
		p = b
		position = 0
	}

	// Append value to entries, linking the bucket to the entires list.
	d.entries = append(d.entries, osdEntry{
		hash:  h,
		key:   k,
		value: v,
	})
	p.entries[position] = &d.entries[len(d.entries)-1]

	return nil
}

func OrderStringDict(d StringDict) OrderedStringDict {
	var osd OrderedStringDict
	osd.init(len(d))

	// Append values in key order.
	for _, key := range d.Keys() {
		h := hashString(key)
		osd.append(h, key, d[key])
	}
	return osd
}

func (d *OrderedStringDict) Set(k string, v Value) (found bool) {
	h := hashString(k)
	if e := d.getEntry(h, k); e != nil {
		e.value = v
		return true
	}
	return false
}

func (d *OrderedStringDict) Get(k string) (v Value, found bool) {
	h := hashString(k)
	if e := d.getEntry(h, k); e != nil {
		return e.value, true
	}
	return None, false
}

func (d *OrderedStringDict) Keys() []string {
	keys := make([]string, 0, len(d.entries))
	for _, e := range d.entries {
		keys = append(keys, e.key)
	}
	return keys
}

func (d *OrderedStringDict) Index(i int) Value {
	return d.entries[i].value
}
func (d *OrderedStringDict) Len() int {
	return len(d.entries)
}
func (d *OrderedStringDict) KeyIndex(i int) (string, Value) {
	e := &d.entries[i]
	return e.key, e.value
}
func (d *OrderedStringDict) Range(f func(key string, value Value) bool) {
	for _, e := range d.entries {
		if !f(e.key, e.value) {
			break
		}
	}
}
