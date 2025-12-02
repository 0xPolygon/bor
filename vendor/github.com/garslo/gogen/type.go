package gogen

type Type struct {
	Name        string // Optional, named type
	TypeName    string
	PackageName string // Optional
}

type Types []Type
