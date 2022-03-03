// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package starlarkstruct

import (
	"fmt"
	_ "unsafe" // for go:linkname hack

	"go.starlark.net/starlark"
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
	hash     uint32 // nonzero => in use
	key      string
	value    starlark.Value
	next     *entry  // insertion order doubly-linked list; may be nil
	prevLink **entry // address of link to this entry (perhaps &head)
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
		for i := range ht.table {
			for p := &ht.table[i]; p != nil; p = p.next {
				for i := range p.entries {
					e := &p.entries[i]
					if e.hash != 0 {
						e.value.Freeze()
					}
				}
			}
		}
	}
}

func (ht *hashtable) insert(k string, v starlark.Value) error {
	if ht.frozen {
		return fmt.Errorf("cannot insert into frozen hash table")
	}
	if ht.itercount > 0 {
		return fmt.Errorf("cannot insert into hash table during iteration")
	}
	if ht.table == nil {
		ht.init(1)
	}
	h, err := starlark.String(k).Hash()
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
			if k != e.key {
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
		// No space in existing buckets.  Add a new one to the bucket list.
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

func (ht *hashtable) lookup(k string) (v starlark.Value, found bool, err error) {
	h, err := starlark.String(k).Hash()
	if err != nil {
		return nil, false, err // unhashable
	}
	if h == 0 {
		h = 1 // zero is reserved
	}
	if ht.table == nil {
		return starlark.None, false, nil // empty
	}

	// Inspect each bucket in the bucket list.
	for p := &ht.table[h&(uint32(len(ht.table)-1))]; p != nil; p = p.next {
		for i := range p.entries {
			e := &p.entries[i]
			if e.hash == h {
				if k == e.key {
					return e.value, true, nil // found
				}
			}
		}
	}
	return starlark.None, false, nil // not found
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
