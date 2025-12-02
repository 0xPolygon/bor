// Copyright 2017 Cameron Bergoon
// Licensed under the MIT License, see LICENCE file for details.

/*Package merkletree implements a Merkle Tree capable of storing arbitrary content.

A Merkle Tree is a hash tree that provides an efficient way to verify the contents
of a set data are present and untampered with. At its core, a Merkle Tree is
a list of items representing the data that should be verified. Each of these items
is inserted into a leaf node and a tree of hashes is constructed bottom up using a
hash of the nodes left and right children's hashes. This means that the root node
will effictively be a hash of all other nodes (hashes) in the tree. This property
allows the tree to be reproduced and thus verified by on the hash of the root node
of the tree. The benefit of the tree structure is verifying any single content
entry in the tree will require only nlog2(n) steps in the worst case.

Creating a new merkletree requires that the type that the tree will be constructed
from implements the Content interface.

	type Content interface {
		CalculateHash() []byte
		Equals(other Content) bool
	}

A slice of the Content items should be created and then passed to the NewTree method.

	t, err := merkle.NewTree(list)

t represents the Merkle Tree and can be verified and manipulated with the API methods
described below.*/
package merkletree
