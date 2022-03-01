// Copyright 2018 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package starlarkstruct_test

import (
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/starlarktest"
	"go.starlark.net/syntax"
)

func Test(t *testing.T) {
	testdata := starlarktest.DataFile("starlarkstruct", ".")
	thread := &starlark.Thread{Load: load}
	starlarktest.SetReporter(thread, t)
	filename := filepath.Join(testdata, "testdata/struct.star")
	predeclared := starlark.StringDict{
		"struct": starlark.NewBuiltin("struct", starlarkstruct.Make),
		"gensym": starlark.NewBuiltin("gensym", gensym),
	}
	if _, err := starlark.ExecFile(thread, filename, nil, predeclared); err != nil {
		if err, ok := err.(*starlark.EvalError); ok {
			t.Fatal(err.Backtrace())
		}
		t.Fatal(err)
	}
}

// load implements the 'load' operation as used in the evaluator tests.
func load(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	if module == "assert.star" {
		return starlarktest.LoadAssertModule()
	}
	return nil, fmt.Errorf("load not implemented")
}

// gensym is a built-in function that generates a unique symbol.
func gensym(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	if err := starlark.UnpackArgs("gensym", args, kwargs, "name", &name); err != nil {
		return nil, err
	}
	return &symbol{name: name}, nil
}

// A symbol is a distinct value that acts as a constructor of "branded"
// struct instances, like a class symbol in Python or a "provider" in Bazel.
type symbol struct{ name string }

var _ starlark.Callable = (*symbol)(nil)

func (sym *symbol) Name() string          { return sym.name }
func (sym *symbol) String() string        { return sym.name }
func (sym *symbol) Type() string          { return "symbol" }
func (sym *symbol) Freeze()               {} // immutable
func (sym *symbol) Truth() starlark.Bool  { return starlark.True }
func (sym *symbol) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", sym.Type()) }

func (sym *symbol) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("%s: unexpected positional arguments", sym)
	}
	return starlarkstruct.FromKeywords(sym, kwargs), nil
}

func benchmarkAttrSmall(b *testing.B, size int) {
	var keys []string
	m := make(starlark.StringDict)
	for i := 0; i < size; i++ {
		key := strconv.Itoa(i)
		m[key] = starlark.Bool(true)
		keys = append(keys, key)
	}
	s := starlarkstruct.FromStringDict(starlarkstruct.Default, m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i%len(keys)]
		_, err := s.Attr(key)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAttr_4(b *testing.B)   { benchmarkAttrSmall(b, 4) }
func BenchmarkAttr_8(b *testing.B)   { benchmarkAttrSmall(b, 8) }
func BenchmarkAttr_16(b *testing.B)  { benchmarkAttrSmall(b, 16) }
func BenchmarkAttr_32(b *testing.B)  { benchmarkAttrSmall(b, 32) }
func BenchmarkAttr_64(b *testing.B)  { benchmarkAttrSmall(b, 64) }
func BenchmarkAttr_128(b *testing.B) { benchmarkAttrSmall(b, 128) }

func benchmarkEqual(b *testing.B, size int) {
	m := make(starlark.StringDict)
	for i := 0; i < size; i++ {
		key := strconv.Itoa(i)
		m[key] = starlark.Bool(true)
	}
	x := starlarkstruct.FromStringDict(starlarkstruct.Default, m)
	y := starlarkstruct.FromStringDict(starlarkstruct.Default, m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := x.CompareSameType(syntax.EQL, y, 40)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEqual_4(b *testing.B)   { benchmarkEqual(b, 4) }
func BenchmarkEqual_8(b *testing.B)   { benchmarkEqual(b, 8) }
func BenchmarkEqual_16(b *testing.B)  { benchmarkEqual(b, 16) }
func BenchmarkEqual_32(b *testing.B)  { benchmarkEqual(b, 32) }
func BenchmarkEqual_64(b *testing.B)  { benchmarkEqual(b, 64) }
func BenchmarkEqual_128(b *testing.B) { benchmarkEqual(b, 128) }
