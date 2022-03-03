// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package starlarkstruct defines the Starlark types 'struct' and
// 'module', both optional language extensions.
//
package starlarkstruct // import "go.starlark.net/starlarkstruct"

// It is tempting to introduce a variant of Struct that is a wrapper
// around a Go struct value, for stronger typing guarantees and more
// efficient and convenient field lookup. However:
// 1) all fields of Starlark structs are optional, so we cannot represent
//    them using more specific types such as String, Int, *Depset, and
//    *File, as such types give no way to represent missing fields.
// 2) the efficiency gain of direct struct field access is rather
//    marginal: finding the index of a field by map access is O(1)
//    and is quite fast compared to the other overheads.
// Such an implementation is likely to be more compact than
// the current map-based representation, though.

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Make is the implementation of a built-in function that instantiates
// an immutable struct from the specified keyword arguments.
//
// An application can add 'struct' to the Starlark environment like so:
//
// 	globals := starlark.StringDict{
// 		"struct":  starlark.NewBuiltin("struct", starlarkstruct.Make),
// 	}
//
func Make(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("struct: unexpected positional arguments")
	}
	return FromKeywords(Default, kwargs), nil
}

// FromKeywords returns a new struct instance whose fields are specified by the
// key/value pairs in kwargs.  (Each kwargs[i][0] must be a starlark.String.)
func FromKeywords(constructor starlark.Value, kwargs []starlark.Tuple) *Struct {
	if constructor == nil {
		panic("nil constructor")
	}

	d := make(starlark.StringDict, len(kwargs))
	for _, kwarg := range kwargs {
		k := string(kwarg[0].(starlark.String))
		v := kwarg[1]
		d[k] = v
	}
	return FromStringDict(constructor, d)
}

// FromStringDict returns a new struct instance whose elements are those of d.
// The constructor parameter specifies the constructor; use Default for an ordinary struct.
func FromStringDict(constructor starlark.Value, d starlark.StringDict) *Struct {
	if constructor == nil {
		panic("nil constructor")
	}

	s := &Struct{constructor: constructor}
	s.ht.init(len(d))
	for _, k := range d.Keys() {
		s.ht.insert(k, d[k])
	}
	return s
}

// Struct is an immutable Starlark type that maps field names to values.
// It is not iterable and does not support len.
//
// A struct has a constructor, a distinct value that identifies a class
// of structs, and which appears in the struct's string representation.
//
// Operations such as x+y fail if the constructors of the two operands
// are not equal.
//
// The default constructor, Default, is the string "struct", but
// clients may wish to 'brand' structs for their own purposes.
// The constructor value appears in the printed form of the value,
// and is accessible using the Constructor method.
//
// Use Attr to access its fields and AttrNames to enumerate them.
type Struct struct {
	constructor starlark.Value
	ht          hashtable
}

// Default is the default constructor for structs.
// It is merely the string "struct".
const Default = starlark.String("struct")

var (
	_ starlark.HasAttrs  = (*Struct)(nil)
	_ starlark.HasBinary = (*Struct)(nil)
)

// ToStringDict adds a name/value entry to d for each field of the struct.
func (s *Struct) ToStringDict(d starlark.StringDict) {
	for e := s.ht.head; e != nil; e = e.next {
		d[e.key] = e.value
	}
}

func (s *Struct) String() string {
	buf := new(strings.Builder)
	if s.constructor == Default {
		// NB: The Java implementation always prints struct
		// even for Bazel provider instances.
		buf.WriteString("struct") // avoid String()'s quotation
	} else {
		buf.WriteString(s.constructor.String())
	}
	buf.WriteByte('(')
	for e := s.ht.head; e != nil; e = e.next {
		if e != s.ht.head {
			buf.WriteString(", ")
		}
		buf.WriteString(e.key)
		buf.WriteString(" = ")
		buf.WriteString(e.value.String())
	}
	buf.WriteByte(')')
	return buf.String()
}

// Constructor returns the constructor used to create this struct.
func (s *Struct) Constructor() starlark.Value { return s.constructor }

func (s *Struct) Type() string         { return "struct" }
func (s *Struct) Truth() starlark.Bool { return true } // even when empty
func (s *Struct) Hash() (uint32, error) {
	// Same algorithm as Tuple.hash, but with different primes.
	var x, m uint32 = 8731, 9839
	for e := s.ht.head; e != nil; e = e.next {
		k, v := e.key, e.value
		namehash, _ := starlark.String(k).Hash()
		x = x ^ 3*namehash
		y, err := v.Hash()
		if err != nil {
			return 0, err
		}
		x = x ^ y*m
		m += 7349
	}
	return x, nil
}
func (s *Struct) Freeze() { s.ht.freeze() }

func (x *Struct) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	if y, ok := y.(*Struct); ok && op == syntax.PLUS {
		if side == starlark.Right {
			x, y = y, x
		}

		if eq, err := starlark.Equal(x.constructor, y.constructor); err != nil {
			return nil, fmt.Errorf("in %s + %s: error comparing constructors: %v",
				x.constructor, y.constructor, err)
		} else if !eq {
			return nil, fmt.Errorf("cannot add structs of different constructors: %s + %s",
				x.constructor, y.constructor)
		}

		s := &Struct{constructor: x.constructor}
		s.ht.init(x.len() + y.len())

		ex, ey := x.ht.head, y.ht.head
		for ex != nil && ey != nil {
			if ex.key < ey.key {
				s.ht.insert(ex.key, ex.value)
				ex = ex.next
			} else if ex.key == ey.key {
				s.ht.insert(ey.key, ey.value)
				ex, ey = ex.next, ey.next
			} else {
				s.ht.insert(ey.key, ey.value)
				ey = ey.next
			}
		}
		for e := ex; e != nil; e = e.next {
			s.ht.insert(e.key, e.value)
		}
		for e := ey; e != nil; e = e.next {
			s.ht.insert(e.key, e.value)
		}
		return s, nil
	}
	return nil, nil // unhandled
}

// Attr returns the value of the specified field.
func (s *Struct) Attr(name string) (starlark.Value, error) {
	if v, ok, err := s.ht.lookup(name); ok {
		return v, nil
	} else if err != nil {
		return nil, err
	}

	var ctor string
	if s.constructor != Default {
		ctor = s.constructor.String() + " "
	}
	return nil, starlark.NoSuchAttrError(
		fmt.Sprintf("%sstruct has no .%s attribute", ctor, name))
}

func (s *Struct) len() int { return int(s.ht.len) }

// AttrNames returns a new sorted list of the struct fields.
func (s *Struct) AttrNames() []string {
	keys := make([]string, 0, int(s.ht.len))
	for e := s.ht.head; e != nil; e = e.next {
		keys = append(keys, e.key)
	}
	return keys
}

func (x *Struct) CompareSameType(op syntax.Token, y_ starlark.Value, depth int) (bool, error) {
	y := y_.(*Struct)
	switch op {
	case syntax.EQL:
		return structsEqual(x, y, depth)
	case syntax.NEQ:
		eq, err := structsEqual(x, y, depth)
		return !eq, err
	default:
		return false, fmt.Errorf("%s %s %s not implemented", x.Type(), op, y.Type())
	}
}

var errNotEqual = fmt.Errorf("not equal")

func structsEqual(x, y *Struct, depth int) (bool, error) {
	if x.len() != y.len() {
		return false, nil
	}

	if eq, err := starlark.Equal(x.constructor, y.constructor); err != nil {
		return false, fmt.Errorf("error comparing struct constructors %v and %v: %v",
			x.constructor, y.constructor, err)
	} else if !eq {
		return false, nil
	}

	for ex, ey := x.ht.head, y.ht.head; ex != nil; ex, ey = ex.next, ey.next {
		if ex.key != ey.key {
			return false, nil
		}
		if eq, err := starlark.EqualDepth(ex.value, ey.value, depth-1); err != nil {
			return false, err
		} else if !eq {
			return false, nil
		}
	}
	return true, nil
}
