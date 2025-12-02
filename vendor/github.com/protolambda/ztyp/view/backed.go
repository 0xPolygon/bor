package view

import . "github.com/protolambda/ztyp/tree"

type BackedView struct {
	ViewBase
	Hook        BackingHook
	BackingNode Node
}

func (v *BackedView) Copy() (View, error) {
	return v.TypeDef.ViewFromBacking(v.BackingNode, nil) // copy does not propagate changes to the same hook.
}

func (v *BackedView) Default(hook BackingHook) (View, error) {
	return v.TypeDef.ViewFromBacking(v.BackingNode, hook)
}

func (v *BackedView) HashTreeRoot(h HashFn) Root {
	return v.BackingNode.MerkleRoot(h)
}

func (v *BackedView) Backing() Node {
	return v.BackingNode
}

func (v *BackedView) SetBacking(b Node) error {
	v.BackingNode = b
	return v.Hook.PropagateChangeMaybe(b)
}
