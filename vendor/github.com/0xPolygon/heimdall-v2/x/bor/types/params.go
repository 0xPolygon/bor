package types

import "fmt"

// Default parameter values
const (
	DefaultSprintDuration    uint64 = 16
	DefaultSpanDuration      uint64 = 400 * DefaultSprintDuration
	DefaultFirstSpanDuration uint64 = 256
	DefaultProducerCount     uint64 = 4
)

// DefaultParams returns default parameters for bor module
func DefaultParams() Params {
	return Params{
		SprintDuration: DefaultSprintDuration,
		SpanDuration:   DefaultSpanDuration,
		ProducerCount:  DefaultProducerCount,
	}
}

// ValidateBasic checks that the bor parameters have valid values.
func (p Params) ValidateBasic() error {
	if err := validatePositiveIntForParam(p.SprintDuration, "sprint duration"); err != nil {
		return err
	}

	if err := validatePositiveIntForParam(p.SpanDuration, "span duration"); err != nil {
		return err
	}

	if err := validatePositiveIntForParam(p.ProducerCount, "producer count"); err != nil {
		return err
	}

	return nil
}

// validatePositiveIntForParam checks if the provided value is a positive integer
func validatePositiveIntForParam(i interface{}, paramName string) error {
	v, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid type provided %T for bor param %s", i, paramName)
	}

	if v == 0 {
		return fmt.Errorf("invalid value provided %d for bor param %s", v, paramName)
	}

	return nil
}
