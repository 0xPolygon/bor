package helper

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Big batch of reflect-types for topic reconstruction.
var (
	reflectHash    = reflect.TypeOf(common.Hash{})
	reflectAddress = reflect.TypeOf(common.Address{})
	reflectBigInt  = reflect.TypeOf(new(big.Int))
)

// UnpackLog function unpacks the given event into the given log type
func UnpackLog(abiObject *abi.ABI, out interface{}, event string, log *types.Log) error {
	selectedEvent := EventByID(abiObject, log.Topics[0].Bytes())

	if selectedEvent == nil || selectedEvent.Name != event {
		return errors.New("topic event mismatch")
	}

	if len(log.Data) > 0 {
		if err := abiObject.UnpackIntoInterface(out, event, log.Data); err != nil {
			return err
		}
	}

	var indexed abi.Arguments

	for _, arg := range abiObject.Events[event].Inputs {
		if arg.Indexed {
			indexed = append(indexed, arg)
		}
	}

	return parseTopics(out, indexed, log.Topics[1:])
}

// parseTopics converts the indexed topic fields into actual log field values.
//
// Note, dynamic types cannot be reconstructed since they get mapped to Keccak256
// hashes as the topic value!
func parseTopics(out interface{}, fields abi.Arguments, topics []common.Hash) error {
	// sanity-check the fields and topics match up
	if len(fields) != len(topics) {
		return errors.New("topic/field count mismatch")
	}

	// Iterate over all the fields and reconstruct them from topics
	for _, arg := range fields {
		if !arg.Indexed {
			return errors.New("non-indexed field in topic reconstruction")
		}

		field := reflect.ValueOf(out).Elem().FieldByName(capitalize(arg.Name))

		// Try to parse the topic back into the fields based on primitive types
		switch field.Kind() {
		case reflect.Bool:
			if topics[0][common.HashLength-1] == 1 {
				field.Set(reflect.ValueOf(true))
			}
		case reflect.Int8:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(int8(num.Int64())))
		case reflect.Int16:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(int16(num.Int64())))
		case reflect.Int32:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(int32(num.Int64())))
		case reflect.Int64:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(num.Int64()))
		case reflect.Uint8:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(uint8(num.Uint64())))
		case reflect.Uint16:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(uint16(num.Uint64())))
		case reflect.Uint32:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(uint32(num.Uint64())))
		case reflect.Uint64:
			num := new(big.Int).SetBytes(topics[0][:])
			field.Set(reflect.ValueOf(num.Uint64()))
		default:
			// Ran out of plain primitive types, try custom types
			switch field.Type() {
			case reflectHash: // Also covers all dynamic types
				field.Set(reflect.ValueOf(topics[0]))
			case reflectAddress:
				var addr common.Address

				copy(addr[:], topics[0][common.HashLength-common.AddressLength:])

				field.Set(reflect.ValueOf(addr))
			case reflectBigInt:
				num := new(big.Int).SetBytes(topics[0][:])
				field.Set(reflect.ValueOf(num))
			default:
				// Ran out of custom types, try the crazies
				switch {
				case arg.Type.T == abi.FixedBytesTy:
					reflect.Copy(field, reflect.ValueOf(topics[0][common.HashLength-arg.Type.Size:]))

				default:
					return fmt.Errorf("unsupported indexed type: %v", arg.Type)
				}
			}
		}

		topics = topics[1:]
	}

	return nil
}

// capitalize makes a camel-case string which starts with an upper case character.
func capitalize(input string) string {
	for len(input) > 0 && input[0] == '_' {
		input = input[1:]
	}

	if len(input) == 0 {
		return ""
	}

	return toCamelCase(strings.ToUpper(input[:1]) + input[1:])
}

// toCamelCase converts an underscore string to a camel-case string
func toCamelCase(input string) string {
	toUpper := false
	result := ""

	for k, v := range input {
		switch {
		case k == 0:
			result = strings.ToUpper(string(input[0]))
		case toUpper:
			result += strings.ToUpper(string(v))
			toUpper = false
		case v == '_':
			toUpper = true
		default:
			result += string(v)
		}
	}

	return result
}
