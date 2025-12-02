package goethkzg

import "errors"

var (
	ErrBatchLengthCheck   = errors.New("all designated elements in the batch should have the same size")
	ErrNonCanonicalScalar = errors.New("scalar is not canonical when interpreted as a big integer in big-endian")
	ErrInvalidCellID      = errors.New("cell ID should be less than CellsPerExtBlob")
	ErrInvalidRowIndex    = errors.New("row index should be less than the number of row commitments")

	ErrNumCellIDsNotEqualNumCells      = errors.New("number of cell IDs should be equal to the number of cells")
	ErrCellIDsNotUnique                = errors.New("cell IDs are not unique")
	ErrFoundInvalidCellID              = errors.New("cell ID should be less than CellsPerExtBlob")
	ErrNotEnoughCellsForReconstruction = errors.New("not enough cells to perform reconstruction")

	// The following errors indicate that the library constants have not been setup properly.
	// These should never happen unless the library has been incorrectly modified.
	ErrNumCosetEvaluationsCheck   = errors.New("expected number of coset evaluations to be `CellsPerExtBlob`")
	ErrCosetEvaluationLengthCheck = errors.New("expected coset evaluations to have `ScalarsPerCell` number of field elements")
	ErrNumProofsCheck             = errors.New("expected number of proofs to be `CellsPerExtBlob`")
)
