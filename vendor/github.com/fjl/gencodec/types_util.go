// Copyright 2017 Felix Lange <fjl@twurst.com>.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"go/types"
	"io"
	"sort"
	"strconv"
)

// walkNamedTypes runs the callback for all named types contained in the given type.
func walkNamedTypes(typ types.Type, callback func(*types.Named)) {
	switch typ := typ.(type) {
	case *types.Basic:
	case *types.Chan:
		walkNamedTypes(typ.Elem(), callback)
	case *types.Map:
		walkNamedTypes(typ.Key(), callback)
		walkNamedTypes(typ.Elem(), callback)
	case *types.Named:
		callback(typ)
	case *types.Pointer:
		walkNamedTypes(typ.Elem(), callback)
	case *types.Slice:
		walkNamedTypes(typ.Elem(), callback)
	case *types.Array:
		walkNamedTypes(typ.Elem(), callback)
	case *types.Struct:
		for i := 0; i < typ.NumFields(); i++ {
			walkNamedTypes(typ.Field(i).Type(), callback)
		}
	case *types.Interface:
		if typ.NumMethods() > 0 {
			panic("BUG: can't walk non-empty interface")
		}
	default:
		panic(fmt.Errorf("BUG: can't walk %T", typ))
	}
}

func lookupStructType(scope *types.Scope, name string) (*types.Named, error) {
	typ, err := lookupType(scope, name)
	if err != nil {
		return nil, err
	}
	_, ok := typ.Underlying().(*types.Struct)
	if !ok {
		return nil, errors.New("not a struct type")
	}
	return typ, nil
}

func lookupType(scope *types.Scope, name string) (*types.Named, error) {
	obj := scope.Lookup(name)
	if obj == nil {
		return nil, errors.New("no such identifier")
	}
	typ, ok := obj.(*types.TypeName)
	if !ok {
		return nil, errors.New("not a type")
	}
	return typ.Type().(*types.Named), nil
}

func isPointer(typ types.Type) bool {
	_, ok := typ.(*types.Pointer)
	return ok
}

func isNonEmptyInterface(typ types.Type) bool {
	iftype := underlying[*types.Interface](typ)
	return iftype != nil && iftype.NumMethods() > 0
}

func isInterface(typ types.Type) bool {
	return underlying[*types.Interface](typ) != nil
}

func underlyingArray(typ types.Type) *types.Array {
	return underlying[*types.Array](typ)
}

func underlyingSlice(typ types.Type) *types.Slice {
	return underlying[*types.Slice](typ)
}

func underlyingMap(typ types.Type) *types.Map {
	return underlying[*types.Map](typ)
}

func underlying[T types.Type](typ types.Type) T {
	for {
		switch t := typ.(type) {
		case *types.Named:
			typ = t.Underlying()
		case T:
			return t
		default:
			var zero T
			return zero
		}
	}
}

func ensureNilCheckable(typ types.Type) types.Type {
	orig := typ
	named := false
	for {
		switch typ.(type) {
		case *types.Named:
			typ = typ.Underlying()
			named = true
		case *types.Slice, *types.Map:
			if named {
				// Named slices, maps, etc. are special because they can have a custom
				// decoder function that prevents the JSON null value. Wrap them with a
				// pointer to allow null always so required/optional works as expected.
				return types.NewPointer(orig)
			}
			return orig
		case *types.Pointer, *types.Interface:
			return orig
		default:
			return types.NewPointer(orig)
		}
	}
}

// checkConvertible determines whether values of type from can be converted to type to. It
// returns nil if convertible and a descriptive error otherwise.
// See package documentation for this definition of 'convertible'.
func checkConvertible(from, to types.Type) error {
	if types.ConvertibleTo(from, to) {
		return nil
	}
	// Slices.
	sfrom := underlyingSlice(from)
	sto := underlyingSlice(to)
	if sfrom != nil && sto != nil {
		if !types.ConvertibleTo(sfrom.Elem(), sto.Elem()) {
			return fmt.Errorf("slice element type %s is not convertible to %s", sfrom.Elem(), sto.Elem())
		}
		return nil
	}

	// Arrays.
	afrom := underlyingArray(from)
	ato := underlyingArray(to)
	if afrom != nil && sto != nil {
		// Array -> slice
		if !types.ConvertibleTo(afrom.Elem(), sto.Elem()) {
			return fmt.Errorf("array element type %s is not convertible to %s", afrom.Elem(), sto.Elem())
		}
		return nil
	}
	if sfrom != nil && ato != nil {
		// Slice -> array
		if !types.ConvertibleTo(sfrom.Elem(), ato.Elem()) {
			return fmt.Errorf("slice element type %s is not convertible to array element %s", sfrom.Elem(), ato.Elem())
		}
		return nil
	}

	// Maps.
	mfrom := underlyingMap(from)
	mto := underlyingMap(to)
	if mfrom != nil && mto != nil {
		if !types.ConvertibleTo(mfrom.Key(), mto.Key()) {
			return fmt.Errorf("map key type %s is not convertible to %s", mfrom.Key(), mto.Key())
		}
		if !types.ConvertibleTo(mfrom.Elem(), mto.Elem()) {
			return fmt.Errorf("map element type %s is not convertible to %s", mfrom.Elem(), mto.Elem())
		}
		return nil
	}
	return fmt.Errorf("type %s is not convertible to %s", from, to)
}

// fileScope tracks imports and other names at file scope.
type fileScope struct {
	imports       []*types.Package
	importsByName map[string]*types.Package
	importNames   map[string]string
	otherNames    map[string]bool // non-package identifiers
	pkg           *types.Package
	imp           types.Importer
}

func newFileScope(imp types.Importer, pkg *types.Package) *fileScope {
	return &fileScope{otherNames: make(map[string]bool), pkg: pkg, imp: imp}
}

func (s *fileScope) writeImportDecl(w io.Writer) {
	fmt.Fprintln(w, "import (")
	for _, pkg := range s.imports {
		if s.importNames[pkg.Path()] != pkg.Name() {
			fmt.Fprintf(w, "\t%s %q\n", s.importNames[pkg.Path()], pkg.Path())
		} else {
			fmt.Fprintf(w, "\t%q\n", pkg.Path())
		}
	}
	fmt.Fprintln(w, ")")
}

// addImport loads a package and adds it to the import set.
func (s *fileScope) addImport(path string) {
	pkg, err := s.imp.Import(path)
	if err != nil {
		panic(fmt.Errorf("can't import %q: %v", path, err))
	}
	s.insertImport(pkg)
	s.rebuildImports()
}

// addReferences marks all names referenced by typ as used.
func (s *fileScope) addReferences(typ types.Type) {
	walkNamedTypes(typ, func(nt *types.Named) {
		pkg := nt.Obj().Pkg()
		if pkg == s.pkg {
			s.otherNames[nt.Obj().Name()] = true
		} else if pkg != nil {
			s.insertImport(nt.Obj().Pkg())
		}
	})
	s.rebuildImports()
}

// insertImport adds pkg to the list of known imports.
// This method should not be used directly because it doesn't
// rebuild the import name cache.
func (s *fileScope) insertImport(pkg *types.Package) {
	i := sort.Search(len(s.imports), func(i int) bool {
		return s.imports[i].Path() >= pkg.Path()
	})
	if i < len(s.imports) && s.imports[i] == pkg {
		return
	}
	s.imports = append(s.imports[:i], append([]*types.Package{pkg}, s.imports[i:]...)...)
}

// rebuildImports caches the names of imported packages.
func (s *fileScope) rebuildImports() {
	s.importNames = make(map[string]string)
	s.importsByName = make(map[string]*types.Package)
	for _, pkg := range s.imports {
		s.maybeRenameImport(pkg)
	}
}

func (s *fileScope) maybeRenameImport(pkg *types.Package) {
	name := pkg.Name()
	for i := 0; s.isNameTaken(name); i++ {
		name = pkg.Name()
		if i > 0 {
			name += strconv.Itoa(i - 1)
		}
	}
	s.importNames[pkg.Path()] = name
	s.importsByName[name] = pkg
}

// isNameTaken reports whether the given name is used by an import or other identifier.
func (s *fileScope) isNameTaken(name string) bool {
	return s.importsByName[name] != nil || s.otherNames[name] || types.Universe.Lookup(name) != nil
}

// qualify is a types.Qualifier that prepends the (possibly renamed) package name of
// imported types to a type name.
func (s *fileScope) qualify(pkg *types.Package) string {
	if pkg == s.pkg {
		return ""
	}
	return s.packageName(pkg.Path())
}

func (s *fileScope) packageName(path string) string {
	name, ok := s.importNames[path]
	if !ok {
		panic("BUG: missing package " + path)
	}
	return name
}

// funcScope tracks used identifiers in a function. It can create new identifiers that do
// not clash with the parent scope.
type funcScope struct {
	used   map[string]bool
	parent *fileScope
}

func newFuncScope(parent *fileScope) *funcScope {
	return &funcScope{make(map[string]bool), parent}
}

// newIdent creates a new identifier that doesn't clash with any name
// in the scope or its parent file scope.
func (s *funcScope) newIdent(base string) string {
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name += strconv.Itoa(i - 1)
		}
		if !s.parent.isNameTaken(name) && !s.used[name] {
			s.used[name] = true
			return name
		}
	}
}
