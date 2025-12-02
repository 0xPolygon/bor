package goethkzg

import (
	"slices"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/crate-crypto/go-eth-kzg/internal/domain"
	kzgmulti "github.com/crate-crypto/go-eth-kzg/internal/kzg_multi"
)

func (ctx *Context) ComputeCells(blob *Blob, numGoRoutines int) ([CellsPerExtBlob]*Cell, error) {
	polynomial, err := DeserializeBlob(blob)
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, err
	}

	// Bit reverse the polynomial representing the Blob so that it is in normal order
	domain.BitReverse(polynomial)

	// Convert the polynomial in lagrange form to a polynomial in monomial form
	polyCoeff := ctx.domain.IfftFr(polynomial)

	// Extend the polynomial
	cosetEvaluations := ctx.fk20.ComputeExtendedPolynomial(polyCoeff)

	return serializeCells(cosetEvaluations)
}

func (ctx *Context) ComputeCellsAndKZGProofs(blob *Blob, numGoRoutines int) ([CellsPerExtBlob]*Cell, [CellsPerExtBlob]KZGProof, error) {
	polynomial, err := DeserializeBlob(blob)
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, err
	}

	// Bit reverse the polynomial representing the Blob so that it is in normal order
	domain.BitReverse(polynomial)

	// Convert the polynomial in lagrange form to a polynomial in monomial form
	polyCoeff := ctx.domain.IfftFr(polynomial)

	return ctx.computeCellsAndKZGProofsFromPolyCoeff(polyCoeff, numGoRoutines)
}

func (ctx *Context) computeCellsAndKZGProofsFromPolyCoeff(polyCoeff []fr.Element, _ int) ([CellsPerExtBlob]*Cell, [CellsPerExtBlob]KZGProof, error) {
	// Compute all proofs and cells
	proofs, cosetEvaluations, err := kzgmulti.ComputeMultiPointKZGProofs(ctx.fk20, polyCoeff)
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, err
	}

	if len(cosetEvaluations) != CellsPerExtBlob {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, ErrNumCosetEvaluationsCheck
	}
	if len(proofs) != CellsPerExtBlob {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, ErrNumProofsCheck
	}

	// Serialize proofs
	var serializedProofs [CellsPerExtBlob]KZGProof
	for i, proof := range proofs {
		serializedProofs[i] = KZGProof(SerializeG1Point(proof))
	}

	// Serialize Cells
	cells, err := serializeCells(cosetEvaluations)
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, err
	}
	return cells, serializedProofs, nil
}

func serializeCells(cosetEvaluations [][]fr.Element) ([CellsPerExtBlob]*Cell, error) {
	var Cells [CellsPerExtBlob]*Cell
	for i, cosetEval := range cosetEvaluations {
		if len(cosetEval) != scalarsPerCell {
			return [CellsPerExtBlob]*Cell{}, ErrCosetEvaluationLengthCheck
		}
		cosetEvalArr := (*[scalarsPerCell]fr.Element)(cosetEval)

		Cells[i] = serializeEvaluations(cosetEvalArr)
	}

	return Cells, nil
}

func (ctx *Context) RecoverCellsAndComputeKZGProofs(cellIDs []uint64, cells []*Cell, numGoRoutines int) ([CellsPerExtBlob]*Cell, [CellsPerExtBlob]KZGProof, error) {
	if len(cellIDs) != len(cells) {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, ErrNumCellIDsNotEqualNumCells
	}

	// Check that the cell Ids are unique
	if !isUniqueUint64(cellIDs) {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, ErrCellIDsNotUnique
	}

	// Check that each CellId is less than CellsPerExtBlob
	for _, cellID := range cellIDs {
		if cellID >= CellsPerExtBlob {
			return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, ErrFoundInvalidCellID
		}
	}

	// Check that we have enough cells to perform reconstruction
	if len(cellIDs) < ctx.dataRecovery.NumBlocksNeededToReconstruct() {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, ErrNotEnoughCellsForReconstruction
	}

	// Find the missing cell IDs and bit reverse them
	// So that they are in normal order
	missingCellIds := make([]uint64, 0, CellsPerExtBlob)
	for cellID := uint64(0); cellID < CellsPerExtBlob; cellID++ {
		if !slices.Contains(cellIDs, cellID) {
			missingCellIds = append(missingCellIds, (domain.BitReverseInt(cellID, CellsPerExtBlob)))
		}
	}

	// Convert Cells to field elements
	extendedBlob := make([]fr.Element, scalarsPerExtBlob)
	// for each cellId, we get the corresponding cell in cells
	// then use the cellId to place the cell in the correct position in the data(extendedBlob) array
	for i, cellID := range cellIDs {
		cell := cells[i]
		// Deserialize the cell
		cellEvals, err := deserializeCell(cell)
		if err != nil {
			return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, err
		}
		// Place the cell in the correct position in the data array
		copy(extendedBlob[cellID*scalarsPerCell:], cellEvals)
	}
	// Bit reverse the extendedBlob so that it is in normal order
	domain.BitReverse(extendedBlob)

	polyCoeff, err := ctx.dataRecovery.RecoverPolynomialCoefficients(extendedBlob, missingCellIds)
	if err != nil {
		return [CellsPerExtBlob]*Cell{}, [CellsPerExtBlob]KZGProof{}, err
	}

	return ctx.computeCellsAndKZGProofsFromPolyCoeff(polyCoeff, numGoRoutines)
}

func (ctx *Context) VerifyCellKZGProofBatch(commitments []KZGCommitment, cellIndices []uint64, cells []*Cell, proofs []KZGProof) error {
	rowCommitments, rowIndices := deduplicateKZGCommitments(commitments)

	// Check that all components in the batch have the same size, expect the rowCommitments
	batchSize := len(rowIndices)
	lengthsAreEqual := batchSize == len(cellIndices) && batchSize == len(cells) && batchSize == len(proofs)
	if !lengthsAreEqual {
		return ErrBatchLengthCheck
	}

	if batchSize == 0 {
		return nil
	}

	// Check that the row indices do not exceed len(rowCommitments)
	for _, rowIndex := range rowIndices {
		if rowIndex >= uint64(len(rowCommitments)) {
			return ErrInvalidRowIndex
		}
	}

	for _, cellIndex := range cellIndices {
		if cellIndex >= CellsPerExtBlob {
			return ErrInvalidCellID
		}
	}

	commitmentsG1 := make([]bls12381.G1Affine, len(rowCommitments))
	for i := 0; i < len(rowCommitments); i++ {
		comm, err := DeserializeKZGCommitment(rowCommitments[i])
		if err != nil {
			return err
		}
		commitmentsG1[i] = comm
	}
	proofsG1 := make([]bls12381.G1Affine, len(proofs))
	for i := 0; i < len(proofs); i++ {
		proof, err := DeserializeKZGProof(proofs[i])
		if err != nil {
			return err
		}
		proofsG1[i] = proof
	}
	cosetsEvals := make([][]fr.Element, len(cells))
	for i := 0; i < len(cells); i++ {
		cosetEvals, err := deserializeCell(cells[i])
		if err != nil {
			return err
		}
		cosetsEvals[i] = cosetEvals
	}
	return kzgmulti.VerifyMultiPointKZGProofBatch(commitmentsG1, rowIndices, cellIndices, proofsG1, cosetsEvals, ctx.openKey7594)
}

// isUniqueUint64 returns true if the slices contains no duplicate elements
func isUniqueUint64(slice []uint64) bool {
	elementMap := make(map[uint64]bool)

	for _, element := range slice {
		if elementMap[element] {
			// Element already seen
			return false
		}
		// Mark the element as seen
		elementMap[element] = true
	}

	// All elements are unique
	return true
}

// deduplicateCommitments takes a slice of KZGCommitments and returns two slices:
// a deduplicated slice of KZGCommitments and a slice of indices.
//
// Each index in the slice of indices corresponds to the position of the
// commitment in the deduplicated slice.
// When coupled with the deduplicated commitments, one is able to reconstruct
// the input duplicated commitments slice.
//
// Note: This function assumes that KZGCommitment is comparable (i.e., can be used as a map key).
// If KZGCommitment is not directly comparable, you may need to implement a custom key function.
func deduplicateKZGCommitments(original []KZGCommitment) ([]KZGCommitment, []uint64) {
	deduplicatedCommitments := make(map[KZGCommitment]uint64)

	// First pass: build the map and count unique elements
	for _, comm := range original {
		if _, exists := deduplicatedCommitments[comm]; !exists {
			// Assign an index to a commitment, the first time we see it
			deduplicatedCommitments[comm] = uint64(len(deduplicatedCommitments))
		}
	}

	deduplicated := make([]KZGCommitment, len(deduplicatedCommitments))
	indices := make([]uint64, len(original))

	// Second pass: build both deduplicated and indices slices
	for i, comm := range original {
		// Get the unique index for this commitment
		index := deduplicatedCommitments[comm]
		// Add the index into the indices slice
		indices[i] = index
		// Add the commitment to the deduplicated slice
		// If the commitment has already been seen, then
		// this just overwrites it with the same parameter.
		deduplicated[index] = comm
	}

	return deduplicated, indices
}
