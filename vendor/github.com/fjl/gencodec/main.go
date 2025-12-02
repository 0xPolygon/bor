// Copyright 2017 Felix Lange <fjl@twurst.com>.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

/*
Command gencodec generates marshaling methods for struct types.

When gencodec is invoked on a directory and type name, it creates a Go source file
containing JSON, YAML and TOML marshaling methods for the type. The generated methods add
features which the standard json package cannot offer.

	gencodec -type MyType -formats json,yaml,toml -out mytype_json.go

# Struct Tags

The gencodec:"required" tag can be used to generate a presence check for the field.
The generated unmarshaling method returns an error if a required field is missing.

Other struct tags are carried over as is. The "json", "yaml", "toml" tags can be used to
rename a field when marshaling.

Example:

	type foo struct {
		Required string `gencodec:"required"`
		Optional string
		Renamed  string `json:"otherName"`
	}

# Field Type Overrides

An invocation of gencodec can specify an additional 'field override' struct from which
marshaling type replacements are taken. If the override struct contains a field whose name
matches the original type, the generated marshaling methods will use the overridden type
and convert to and from the original field type. If the override struct contains a field F
of type T, which does not exist in the original type, and the original type has a method
named F with no arguments and return type assignable to T, the method is called by Marshal*.
If there is a matching method F but the return type or arguments are unsuitable, an error
is raised.

In this example, the specialString type implements json.Unmarshaler to enforce additional
parsing rules. When json.Unmarshal is used with type foo, the specialString unmarshaler
will be used to parse the value of SpecialField. The result of foo.Func() is added to the
result on marshaling under the key `id`. If the input on unmarshal contains a key `id` this
field is ignored.

	//go:generate gencodec -type foo -field-override fooMarshaling -out foo_json.go

	type foo struct {
		Field        string
		SpecialField string
	}

	func (f foo) Func() string {
		return f.Field + "-" + f.SpecialField
	}

	type fooMarshaling struct {
		SpecialField specialString // overrides type of SpecialField when marshaling/unmarshaling
		Func string `json:"id"`    // adds the result of foo.Func() to the serialised object under the key id
	}

# Relaxed Field Conversions

Field types in the override struct must be trivially convertible to the original field
type. gencodec's definition of 'convertible' is less restrictive than the usual rules
defined in the Go language specification.

The following conversions are supported:

If the fields are directly assignable, no conversion is emitted. If the fields are
convertible according to Go language rules, a simple conversion is emitted. Example input
code:

	type specialString string

	func (s *specialString) UnmarshalJSON(input []byte) error { ... }

	type Foo struct{ S string }

	type fooMarshaling struct{ S specialString }

The generated code will contain:

	func (f *Foo) UnmarshalJSON(input []byte) error {
		var dec struct{ S *specialString }
		...
		f.S = string(dec.specialString)
		...
	}

# Array/Slice/Map conversions

If the fields are of map or slice type and the element (and key) types are convertible, a
simple loop is emitted. Example input code:

	type Foo2 struct{ M map[string]string }

	type foo2Marshaling struct{ S map[string]specialString }

The generated code is similar to this snippet:

	func (f *Foo2) UnmarshalJSON(input []byte) error {
		var dec struct{ M map[string]specialString }
		...
		for k, v := range dec.M {
			f.M[k] = string(v)
		}
		...
	}

Conversions between slices and arrays are supported. Example input code:

	type Foo3 struct{ A [2]string }

	type foo3Marshaling struct{ A []string }

The generated code is similar to this snippet:

	func (f *Foo3) UnmarshalJSON(input []byte) error {
		var dec struct{ A []string }
		...
		for i, v := range dec.A {
			f.A[i] = string(v)
		}
		...
	}

# Non-empty interfaces

For some use cases, like configuration loading, you may wish to work with structs
containing fields with a non-empty interface type. Package json supports encoding such
types (it simply encodes the concrete type contained in the interface), but decoding is
not supported. With gencodec, you can use the override struct to assign a concrete type
that will be used for the interface.

In the example below, the encoded struct has a field of type io.Writer. During decoding,
the input is verified to be "stdout" or "stderr", and the appropriate writer is inserted.

	//go:generate gencodec -type Config -field-override configMarshaling -formats json -out output.go

	type Config struct{
		Output io.Writer
	}

	type configMarshaling struct{
		Output configWriter
	}

	type configWriter struct{
		io.Writer
	}

	func (w *configWriter) UnmarshalJSON(input []byte) error {
		var outputName string
		if err := json.Unmarshal(input, &outputName); err != nil {
			return err
		}
		switch outputName {
		case "stdout":
			w.Writer = os.Stdout
		case "stderr":
			w.Writer = os.Stderr
		default:
			return errors.New("invalid output name")
		}
		return nil
	}
*/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/garslo/gogen"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

func main() {
	var (
		pkgdir    = flag.String("dir", ".", "input package")
		output    = flag.String("out", "-", "output file (default is stdout)")
		typename  = flag.String("type", "", "type to generate methods for")
		overrides = flag.String("field-override", "", "type to take field type replacements from")
		formats   = flag.String("formats", "json", `marshaling formats (e.g. "json,yaml")`)
	)
	flag.Parse()

	formatList := strings.Split(*formats, ",")
	for i := range formatList {
		formatList[i] = strings.TrimSpace(formatList[i])
	}
	cfg := Config{Dir: *pkgdir, Type: *typename, FieldOverride: *overrides, Formats: formatList}
	code, err := cfg.process()
	if err != nil {
		fatal(err)
	}
	if *output == "-" {
		os.Stdout.Write(code)
	} else if err := os.WriteFile(*output, code, 0644); err != nil {
		fatal(err)
	}
}

func fatal(args ...interface{}) {
	fmt.Fprintln(os.Stderr, args...)
	os.Exit(1)
}

var AllFormats = []string{"json", "yaml", "toml"}

type Config struct {
	Dir           string   // input package directory
	Type          string   // type to generate methods for
	FieldOverride string   // name of struct type for field overrides
	Formats       []string // defaults to just "json", supported: "json", "yaml"
	Importer      types.Importer
	FileSet       *token.FileSet
}

func (cfg *Config) process() (code []byte, err error) {
	if cfg.FileSet == nil {
		cfg.FileSet = token.NewFileSet()
	}
	if cfg.Importer == nil {
		cfg.Importer = importer.Default()
	}
	if cfg.Formats == nil {
		cfg.Formats = []string{"json"}
	}
	pkg, err := loadPackage(cfg)
	if err != nil {
		return nil, err
	}
	typ, err := lookupStructType(pkg.Scope(), cfg.Type)
	if err != nil {
		return nil, fmt.Errorf("can't find %s in %q: %v", cfg.Type, pkg.Path(), err)
	}

	// Construct the marshaling type.
	mtyp := newMarshalerType(cfg.FileSet, cfg.Importer, typ)
	if cfg.FieldOverride != "" {
		otyp, err := lookupStructType(pkg.Scope(), cfg.FieldOverride)
		if err != nil {
			return nil, fmt.Errorf("can't find field replacement type %s: %v", cfg.FieldOverride, err)
		}

		err = mtyp.loadOverrides(otyp)
		if err != nil {
			return nil, err
		}
	}

	// Generate and format the output. Formatting uses goimports because it
	// removes unused imports.
	code, err = generate(mtyp, cfg)
	if err != nil {
		return nil, err
	}
	opt := &imports.Options{Comments: true, TabIndent: true, TabWidth: 8}
	code, err = imports.Process("", code, opt)
	if err != nil {
		panic(fmt.Errorf("BUG: can't gofmt generated code: %v", err))
	}
	return code, nil
}

func loadPackage(cfg *Config) (*types.Package, error) {
	pcfg := &packages.Config{
		Mode:  packages.NeedTypes | packages.NeedDeps | packages.NeedImports,
		Tests: true,
		Dir:   cfg.Dir,
	}
	ps, err := packages.Load(pcfg, ".")
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("can't find go package in %s", cfg.Dir)
	}
	return ps[0].Types, nil
}

func generate(mtyp *marshalerType, cfg *Config) ([]byte, error) {
	w := new(bytes.Buffer)
	fmt.Fprint(w, "// Code generated by github.com/fjl/gencodec. DO NOT EDIT.\n\n")
	fmt.Fprintln(w, "package", mtyp.orig.Obj().Pkg().Name())
	fmt.Fprintln(w)
	mtyp.scope.writeImportDecl(w)
	fmt.Fprintln(w)
	if mtyp.override != nil {
		writeUseOfOverride(w, mtyp.override, mtyp.scope.qualify)
	}
	for _, format := range cfg.Formats {
		var genMarshal, genUnmarshal gogen.Function
		switch format {
		case "json":
			genMarshal = genMarshalJSON(mtyp)
			genUnmarshal = genUnmarshalJSON(mtyp)
		case "yaml":
			genMarshal = genMarshalYAML(mtyp)
			genUnmarshal = genUnmarshalYAML(mtyp)
		case "toml":
			genMarshal = genMarshalTOML(mtyp)
			genUnmarshal = genUnmarshalTOML(mtyp)
		default:
			return nil, fmt.Errorf("unknown format: %q", format)
		}
		fmt.Fprintf(w, "// %s marshals as %s.", genMarshal.Name, strings.ToUpper(format))
		fmt.Fprintln(w)
		writeFunction(w, mtyp.fs, genMarshal)
		fmt.Fprintln(w)
		fmt.Fprintf(w, "// %s unmarshals from %s.", genUnmarshal.Name, strings.ToUpper(format))
		fmt.Fprintln(w)
		writeFunction(w, mtyp.fs, genUnmarshal)
		fmt.Fprintln(w)
	}
	return w.Bytes(), nil
}

func writeUseOfOverride(w io.Writer, n *types.Named, qf types.Qualifier) {
	name := types.TypeString(types.NewPointer(n), qf)
	fmt.Fprintf(w, "var _ = (%s)(nil)\n", name)
}

// marshalerType represents the intermediate struct type used during marshaling.
// This is the input data to all the Go code templates.
type marshalerType struct {
	name     string
	Fields   []*marshalerField
	fs       *token.FileSet
	orig     *types.Named
	override *types.Named
	scope    *fileScope
}

// marshalerField represents a field of the intermediate marshaling type.
type marshalerField struct {
	name     string
	typ      types.Type
	origTyp  types.Type
	tag      string
	function *types.Func // map to a function instead of a field
}

func newMarshalerType(fs *token.FileSet, imp types.Importer, typ *types.Named) *marshalerType {
	mtyp := &marshalerType{name: typ.Obj().Name(), fs: fs, orig: typ}
	styp := typ.Underlying().(*types.Struct)
	mtyp.scope = newFileScope(imp, typ.Obj().Pkg())
	mtyp.scope.addReferences(styp)

	// Add packages which are always needed.
	mtyp.scope.addImport("encoding/json")
	mtyp.scope.addImport("errors")

	for i := 0; i < styp.NumFields(); i++ {
		f := styp.Field(i)
		if !f.Exported() {
			continue
		}
		if f.Anonymous() {
			fmt.Fprintf(os.Stderr, "Warning: ignoring embedded field %s\n", f.Name())
			continue
		}

		mf := &marshalerField{
			name:    f.Name(),
			typ:     f.Type(),
			origTyp: f.Type(),
			tag:     styp.Tag(i),
		}

		mtyp.Fields = append(mtyp.Fields, mf)
	}

	return mtyp
}

// findFunction returns a function with `name` that accepts no arguments
// and returns a single value that is convertible to the given to type.
func findFunction(typ *types.Named, name string, to types.Type) (*types.Func, types.Type) {
	for i := 0; i < typ.NumMethods(); i++ {
		fun := typ.Method(i)
		if fun.Name() != name || !fun.Exported() {
			continue
		}
		sign := fun.Type().(*types.Signature)
		if sign.Params().Len() != 0 || sign.Results().Len() != 1 {
			continue
		}
		if err := checkConvertible(sign.Results().At(0).Type(), to); err == nil {
			return fun, sign.Results().At(0).Type()
		}
	}
	return nil, nil
}

// loadOverrides sets field types of the intermediate marshaling type from
// matching fields of otyp.
func (mtyp *marshalerType) loadOverrides(otyp *types.Named) error {
	s := otyp.Underlying().(*types.Struct)
	for i := 0; i < s.NumFields(); i++ {
		of := s.Field(i)
		if of.Anonymous() || !of.Exported() {
			return fmt.Errorf("%v: field override type cannot have embedded or unexported fields", mtyp.fs.Position(of.Pos()))
		}
		f := mtyp.fieldByName(of.Name())
		if f == nil {
			// field not defined in original type, check if it maps to a suitable function and add it as an override
			if fun, retType := findFunction(mtyp.orig, of.Name(), of.Type()); fun != nil {
				f = &marshalerField{name: fun.Name(), origTyp: retType, typ: of.Type(), function: fun, tag: s.Tag(i)}
				mtyp.Fields = append(mtyp.Fields, f)
			} else {
				return fmt.Errorf("%v: no matching field or function for %s in original type %s", mtyp.fs.Position(of.Pos()), of.Name(), mtyp.name)
			}
		}
		if err := checkConvertible(of.Type(), f.origTyp); err != nil {
			return fmt.Errorf("%v: invalid field override: %v", mtyp.fs.Position(of.Pos()), err)
		}
		f.typ = of.Type()
	}
	mtyp.scope.addReferences(s)
	mtyp.override = otyp
	return nil
}

func (mtyp *marshalerType) fieldByName(name string) *marshalerField {
	for _, f := range mtyp.Fields {
		if f.name == name {
			return f
		}
	}
	return nil
}

// isRequired returns whether the field is required when decoding the given format.
func (mf *marshalerField) isRequired(format string) bool {
	rtag := reflect.StructTag(mf.tag)
	req := rtag.Get("gencodec") == "required"
	// Fields with json:"-" must be treated as optional. This also works
	// for the other supported formats.
	return req && !strings.HasPrefix(rtag.Get(format), "-")
}

// encodedName returns the alternative field name assigned by the format's struct tag.
func (mf *marshalerField) encodedName(format string) string {
	val := reflect.StructTag(mf.tag).Get(format)
	if comma := strings.Index(val, ","); comma != -1 {
		val = val[:comma]
	}
	if val == "" || val == "-" {
		return uncapitalize(mf.name)
	}
	return val
}

func uncapitalize(s string) string {
	return strings.ToLower(s[:1]) + s[1:]
}
