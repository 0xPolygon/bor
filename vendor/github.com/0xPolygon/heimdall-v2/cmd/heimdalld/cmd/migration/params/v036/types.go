package v036

import (
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/codec"

	v036gov "github.com/0xPolygon/heimdall-v2/cmd/heimdalld/cmd/migration/gov/v036"
)

const (
	// RouterKey defines the routing key for a ParameterChangeProposal
	RouterKey = "params"
	// ProposalTypeChange defines the type for a ParameterChangeProposal
	ProposalTypeChange = "ParameterChange"
	// DefaultCodespace defines the param module codespace name
	DefaultCodespace = "params"
)

// Assert ParameterChangeProposal implements v036gov.Content at compile-time
var _ v036gov.Content = ParameterChangeProposal{}

// ParameterChangeProposal defines a proposal which contains multiple parameter
// changes.
type ParameterChangeProposal struct {
	Title       string        `json:"title" yaml:"title"`
	Description string        `json:"description" yaml:"description"`
	Changes     []ParamChange `json:"changes" yaml:"changes"`
}

// GetTitle returns the title of a parameter change proposal.
func (pcp ParameterChangeProposal) GetTitle() string { return pcp.Title }

// GetDescription returns the description of a parameter change proposal.
func (pcp ParameterChangeProposal) GetDescription() string { return pcp.Description }

// ProposalRoute returns the routing key of a parameter change proposal.
func (pcp ParameterChangeProposal) ProposalRoute() string { return RouterKey }

// ProposalType returns the type of the parameter change proposal.
func (pcp ParameterChangeProposal) ProposalType() string { return ProposalTypeChange }

// ValidateBasic validates the parameter change proposal
func (pcp ParameterChangeProposal) ValidateBasic() error {
	err := v036gov.ValidateAbstract(pcp)
	if err != nil {
		return err
	}

	return ValidateChanges(pcp.Changes)
}

// String implements the Stringer interface.
func (pcp ParameterChangeProposal) String() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf(`Parameter Change Proposal:
  Title:       %s
  Description: %s
  Changes:
`, pcp.Title, pcp.Description))

	for _, pc := range pcp.Changes {
		b.WriteString(fmt.Sprintf(`    Param Change:
      Subspace: %s
      Key:      %s
      Subkey:   %X
      Value:    %X
`, pc.Subspace, pc.Key, pc.Subkey, pc.Value))
	}

	return b.String()
}

// ParamChange defines a parameter change.
type ParamChange struct {
	Subspace string `json:"subspace" yaml:"subspace"`
	Key      string `json:"key" yaml:"key"`
	Subkey   string `json:"subkey,omitempty" yaml:"subkey,omitempty"`
	Value    string `json:"value" yaml:"value"`
}

// String implements the Stringer interface.
func (pc ParamChange) String() string {
	return fmt.Sprintf(`Param Change:
  Subspace: %s
  Key:      %s
  Subkey:   %X
  Value:    %X
`, pc.Subspace, pc.Key, pc.Subkey, pc.Value)
}

// ValidateChanges performs basic validation checks over a set of ParamChange. It
// returns an error if any ParamChange is invalid.
func ValidateChanges(changes []ParamChange) error {
	if len(changes) == 0 {
		return ErrEmptyChanges(DefaultCodespace)
	}

	for _, pc := range changes {
		if len(pc.Subspace) == 0 {
			return ErrEmptySubspace(DefaultCodespace)
		}
		if len(pc.Key) == 0 {
			return ErrEmptyKey(DefaultCodespace)
		}
		if len(pc.Value) == 0 {
			return ErrEmptyValue(DefaultCodespace)
		}
	}

	return nil
}

// ErrEmptyChanges returns an error for empty parameter changes.
func ErrEmptyChanges(_ string) error {
	return fmt.Errorf("submitted parameter changes are empty")
}

// ErrEmptySubspace returns an error for an empty subspace.
func ErrEmptySubspace(_ string) error {
	return fmt.Errorf("parameter subspace is empty")
}

// ErrEmptyKey returns an error for when an empty key is given.
func ErrEmptyKey(_ string) error {
	return fmt.Errorf("parameter key is empty")
}

// ErrEmptyValue returns an error for when an empty key is given.
func ErrEmptyValue(_ string) error {
	return fmt.Errorf("parameter value is empty")
}

func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(ParameterChangeProposal{}, "heimdall/ParameterChangeProposal", nil)
}
