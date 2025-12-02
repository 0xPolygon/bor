package codec

import (
	"encoding/binary"
	"fmt"
	"github.com/protolambda/ztyp/bitfields"
	"io"
	"io/ioutil"
)

type Deserializable interface {
	Deserialize(dr *DecodingReader) error
	FixedLength
}

type FixedLength interface {
	FixedLength() uint64
}

type Decodable interface {
	Decode(x []byte) error
}

type DecodingReader struct {
	input   io.Reader
	i       uint64
	max     uint64
	scratch [32]byte
}

func NewDecodingReader(input io.Reader, scope uint64) *DecodingReader {
	return &DecodingReader{input: io.LimitReader(input, int64(scope)), i: 0, max: scope}
}

// SubScope returns a scope of the SSZ reader. Re-uses same scratchpad.
func (dr *DecodingReader) SubScope(count uint64) (*DecodingReader, error) {
	// TODO: based on scope, read a buffer ahead of time.
	if span := dr.Scope(); span < count {
		return nil, fmt.Errorf("cannot create scoped decoding reader, scope of %d bytes is bigger than parent scope has available space %d", count, span)
	}
	// TODO: don't nest readers, instead just limit input reads ourselves
	return &DecodingReader{input: io.LimitReader(dr.input, int64(count)), i: 0, max: count}, nil
}

func (dr *DecodingReader) UpdateIndexFromScoped(other *DecodingReader) {
	dr.i += other.i
}

// how far we have read so far (scoped per container)
func (dr *DecodingReader) Index() uint64 {
	return dr.i
}

// How far we can read (max - i = remaining bytes to read without error).
// Note: when a child element is not fixed length,
// the parent should set the scope, so that the child can infer its size from it.
func (dr *DecodingReader) Max() uint64 {
	return dr.max
}

func (dr *DecodingReader) hasScope(x uint64) error {
	if ^uint64(0)-dr.i < x {
		return fmt.Errorf("overflow: x: %d, i: %d, max: %d", x, dr.i, dr.max)
	}
	v := dr.i + x
	if v > dr.max {
		return fmt.Errorf("cannot read %d bytes, %d beyond scope", x, v-dr.max)
	}
	return nil
}

func (dr *DecodingReader) checkedIndexUpdate(x uint64) (n int, err error) {
	if ^uint64(0)-dr.i < x {
		return 0, fmt.Errorf("overflow: x: %d, i: %d, max: %d", x, dr.i, dr.max)
	}
	v := dr.i + x
	if v > dr.max {
		return int(dr.i), fmt.Errorf("cannot read %d bytes, %d beyond scope", x, v-dr.max)
	}
	dr.i = v
	return int(x), nil
}

func (dr *DecodingReader) Skip(count uint64) (int, error) {
	if n, err := dr.checkedIndexUpdate(count); err != nil {
		return n, err
	}
	switch r := dr.input.(type) {
	case io.Seeker:
		n, err := r.Seek(int64(count), io.SeekCurrent)
		return int(n), err
	default:
		n, err := io.CopyN(ioutil.Discard, dr.input, int64(count))
		return int(n), err
	}
}

// Read p fully, returns n read bytes. len(p) == n always if err == nil
func (dr *DecodingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if n, err := dr.checkedIndexUpdate(uint64(len(p))); err != nil {
		return n, err
	}
	n := 0
	for n < len(p) {
		v, err := dr.input.Read(p[n:])
		n += v
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (dr *DecodingReader) ReadByte() (byte, error) {
	_, err := dr.Read(dr.scratch[0:1])
	return dr.scratch[0], err
}

func (dr *DecodingReader) ReadUint16() (uint16, error) {
	_, err := dr.Read(dr.scratch[0:2])
	return binary.LittleEndian.Uint16(dr.scratch[0:2]), err
}

func (dr *DecodingReader) ReadUint32() (uint32, error) {
	_, err := dr.Read(dr.scratch[0:4])
	return binary.LittleEndian.Uint32(dr.scratch[0:4]), err
}

func (dr *DecodingReader) ReadUint64() (uint64, error) {
	_, err := dr.Read(dr.scratch[0:8])
	return binary.LittleEndian.Uint64(dr.scratch[0:8]), err
}

// returns the remaining scope (amount of bytes that can be read)
func (dr *DecodingReader) Scope() uint64 {
	return dr.Max() - dr.Index()
}

func (dr *DecodingReader) ReadOffset() (uint32, error) {
	return dr.ReadUint32()
}

// Deserialize vector. If fixedElemSize == 0, the item is regarded as dynamic length
func (dr *DecodingReader) Vector(item func(i uint64) Deserializable, fixedElemSize uint64, length uint64) error {
	if fixedElemSize != 0 {
		for i := uint64(0); i < length; i++ {
			sub, err := dr.SubScope(fixedElemSize)
			if err != nil {
				return err
			}
			if err := item(i).Deserialize(sub); err != nil {
				return fmt.Errorf("failed to deserialize item %d: %v", i, err)
			}
		}
		return nil
	} else {
		scope := dr.Scope()
		// TODO could optimize this
		offsets := make([]uint64, length, length)
		for i := uint64(0); i < length; i++ {
			off, err := dr.ReadOffset()
			if err != nil {
				return err
			}
			offsets[i] = uint64(off)
		}
		var prev uint64
		for i, off := range offsets {
			if prev > off {
				return fmt.Errorf("offset %d is too low, previous was %d", off, prev)
			}
			item := item(uint64(0))
			next := scope
			if len(offsets) > i+1 {
				next = offsets[i+1]
			}
			sub, err := dr.SubScope(next - off)
			if err != nil {
				return err
			}
			if err := item.Deserialize(sub); err != nil {
				return fmt.Errorf("failed to serialize item %d: %v", i, err)
			}
			prev = next
		}
		return nil
	}
}

func (dr *DecodingReader) List(add func() Deserializable, fixedElemSize uint64, limit uint64) error {
	scope := dr.Scope()
	// no items to decode
	if scope == 0 {
		return nil
	}
	if fixedElemSize != 0 {
		if scope%fixedElemSize != 0 {
			return fmt.Errorf("scope %d is not a multiple of expected element size: %d", scope, fixedElemSize)
		}
		length := scope / fixedElemSize
		if length > limit {
			return fmt.Errorf("too many items in list: %d > %d", length, limit)
		}
		for i := uint64(0); i < length; i++ {
			item := add()
			sub, err := dr.SubScope(fixedElemSize)
			if err != nil {
				return err
			}
			if err := item.Deserialize(sub); err != nil {
				return err
			}
		}
		return nil
	} else {
		firstOffset, err := dr.ReadOffset()
		if err != nil {
			return err
		}
		if firstOffset%4 != 0 {
			return fmt.Errorf("first offset of list is invalid, not a multiple of 4: %d", firstOffset)
		}
		length := uint64(firstOffset / 4)
		if length > limit {
			return fmt.Errorf("too many items in list: %d > %d", length, limit)
		}
		// TODO could optimize this
		offsets := make([]uint64, 0, length)
		offsets = append(offsets, uint64(firstOffset))
		for i := uint64(1); i < length; i++ {
			off, err := dr.ReadOffset()
			if err != nil {
				return err
			}
			offsets = append(offsets, uint64(off))
		}
		var prev uint64
		for i, off := range offsets {
			if prev > off {
				return fmt.Errorf("offset %d is too low, previous was %d", off, prev)
			}
			item := add()
			next := scope
			if len(offsets) > i+1 {
				next = offsets[i+1]
			}
			if next == off {
				prev = off
				continue
			}
			sub, err := dr.SubScope(next - off)
			if err != nil {
				return err
			}
			if err := item.Deserialize(sub); err != nil {
				return fmt.Errorf("failed to serialize item %d: %v", i, err)
			}
			prev = off
		}
		return nil
	}
}

func (dr *DecodingReader) BitVector(dst *[]byte, bitLength uint64) error {
	if dst == nil {
		return fmt.Errorf("bitvector destination is nil")
	}
	// grow the destination if necessary
	byteLen := (bitLength + 7) >> 3
	if uint64(cap(*dst)) < byteLen {
		*dst = make([]byte, byteLen, byteLen)
	} else {
		*dst = (*dst)[:byteLen]
	}
	if _, err := dr.Read(*dst); err != nil {
		return err
	}
	return bitfields.BitvectorCheck(*dst, bitLength)
}

func (dr *DecodingReader) BitList(dst *[]byte, bitLimit uint64) error {
	byteLen := dr.Scope()
	if byteLimit := (bitLimit + 7) >> 3; byteLen > byteLimit {
		return fmt.Errorf("bitlist is too big: %d bytes, limit is %d (bitlimit %d)", byteLen, byteLimit, bitLimit)
	}
	// grow the destination if necessary
	if uint64(cap(*dst)) < byteLen {
		*dst = make([]byte, byteLen, byteLen)
	} else {
		*dst = (*dst)[:byteLen]
	}
	if _, err := dr.Read(*dst); err != nil {
		return err
	}
	return bitfields.BitlistCheck(*dst, bitLimit)
}

func (dr *DecodingReader) ByteVector(dst *[]byte, byteLength uint64) error {
	if dst == nil {
		return fmt.Errorf("byte vector destination is nil")
	}
	// grow the destination if necessary
	byteLen := (byteLength + 7) >> 3
	if uint64(cap(*dst)) < byteLen {
		*dst = make([]byte, byteLen, byteLen)
	} else {
		*dst = (*dst)[:byteLen]
	}
	_, err := dr.Read(*dst)
	return err
}

func (dr *DecodingReader) ByteList(dst *[]byte, byteLimit uint64) error {
	byteLen := dr.Scope()
	if byteLen > byteLimit {
		return fmt.Errorf("byte list is too big: %d bytes, limit is %d", byteLen, byteLimit)
	}
	// grow the destination if necessary
	if uint64(cap(*dst)) < byteLen {
		*dst = make([]byte, byteLen, byteLen)
	} else if uint64(len(*dst)) < byteLen {
		*dst = (*dst)[:byteLen]
	}
	_, err := dr.Read(*dst)
	return err
}

// FixedLenContainer just reads the concatednated fields, without opening smaller scopes.
// Much faster for simple structs, but use with caution.
func (dr *DecodingReader) FixedLenContainer(fields ...Deserializable) error {
	for i, f := range fields {
		if err := f.Deserialize(dr); err != nil {
			return fmt.Errorf("failed to deserialize fixed-length field %d: %v", i, err)
		}
	}
	return nil
}

func (dr *DecodingReader) Container(fields ...Deserializable) error {
	scope := dr.Scope()
	var offsets []uint64
	var dynFields []Deserializable
	var prev uint64
	for i, f := range fields {
		if fix := f.FixedLength(); fix != 0 {
			sub, err := dr.SubScope(fix)
			if err != nil {
				return err
			}
			if err := f.Deserialize(sub); err != nil {
				return fmt.Errorf("failed to deserialize fixed-length field %d: %v", i, err)
			}
			prev += fix
		} else {
			off, err := dr.ReadOffset()
			if err != nil {
				return fmt.Errorf("failed to read offset for field %d: %v", i, err)
			}
			offsets = append(offsets, uint64(off))
			dynFields = append(dynFields, f)
			prev += 4
		}
	}
	if len(dynFields) == 0 || len(offsets) == 0 {
		return nil
	}
	if prev != offsets[0] {
		return fmt.Errorf("offset 0 is incorrect, expected %d, got %d", prev, offsets[0])
	}
	for i, off := range offsets {
		f := dynFields[i]
		next := scope
		if len(offsets) > i+1 {
			next = offsets[i+1]
		}
		if next < off {
			return fmt.Errorf("scope cannot be negative, got offset %d after %d, at index %d", next, off, i)
		}
		sub, err := dr.SubScope(next - off)
		if err != nil {
			return err
		}
		if err := f.Deserialize(sub); err != nil {
			return fmt.Errorf("failed to deserialize dynamic-length field %d: %v", i, err)
		}
		prev = next
	}
	return nil
}

// Deserialize a Union, the selectFn is called to retrieve a destination to deserialize into, and remember the selector.
// The selectFn can return a nil when there is nothing to decode (when Union is used as an Optional)
func (dr *DecodingReader) Union(selectFn func(selector uint8) (Deserializable, error)) error {
	selector, err := dr.ReadByte()
	if err != nil {
		return fmt.Errorf("fialed to read union selector: %v", err)
	}
	dest, err := selectFn(selector)
	if err != nil {
		return fmt.Errorf("failed to select union option, with selector %d: %v", selector, err)
	}
	if dest == nil {
		if selector != 0 {
			return fmt.Errorf("only 0 the selector can indicate a None value")
		}
		return nil
	}
	return dest.Deserialize(dr)
}
