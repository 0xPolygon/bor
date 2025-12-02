package codec

import (
	"encoding/binary"
	"fmt"
	"io"
)

type Serializable interface {
	Serialize(w *EncodingWriter) error
	ByteLength
	FixedLength
}

type ByteLength interface {
	ByteLength() uint64
}

const OFFSET_SIZE = 4

func Sum(values ...ByteLength) (out uint64) {
	for _, v := range values {
		out += v.ByteLength()
	}
	return
}

func ContainerLength(values ...Serializable) (out uint64) {
	for _, v := range values {
		if size := v.FixedLength(); size == 0 {
			out += v.ByteLength() + OFFSET_SIZE
		} else {
			out += size
		}
	}
	return
}

type Encodable interface {
	Encode() ([]byte, error)
}

type EncodingWriter struct {
	w       io.Writer
	n       int
	Scratch [32]byte
}

func NewEncodingWriter(w io.Writer) *EncodingWriter {
	return &EncodingWriter{w: w, n: 0}
}

// How many bytes were written to the underlying io.Writer before ending encoding (for handling errors)
func (ew *EncodingWriter) Written() int {
	return ew.n
}

// Write writes len(p) bytes from p fully to the underlying accumulated buffer.
func (ew *EncodingWriter) Write(p []byte) error {
	n := 0
	for n < len(p) {
		d, err := ew.w.Write(p[n:])
		ew.n += d
		if err != nil {
			return err
		}
		n += d
	}
	return nil
}

// Write a single byte to the buffer.
func (ew *EncodingWriter) WriteByte(v byte) error {
	ew.Scratch[0] = v
	return ew.Write(ew.Scratch[0:1])
}

// Writes an offset for an element
func (ew *EncodingWriter) WriteOffset(prevOffset uint64, elemLen uint64) (offset uint64, err error) {
	if prevOffset >= (uint64(1) << 32) {
		panic("cannot write offset with invalid previous offset")
	}
	if elemLen >= (uint64(1) << 32) {
		panic("cannot write offset with invalid element size")
	}
	offset = prevOffset + elemLen
	if offset >= (uint64(1) << 32) {
		panic("offset too large, not uint32")
	}
	binary.LittleEndian.PutUint32(ew.Scratch[0:4], uint32(offset))
	err = ew.Write(ew.Scratch[0:4])
	return
}

func (ew *EncodingWriter) WriteUint16(v uint16) error {
	binary.LittleEndian.PutUint16(ew.Scratch[0:2], v)
	return ew.Write(ew.Scratch[0:2])
}

func (ew *EncodingWriter) WriteUint32(v uint32) error {
	binary.LittleEndian.PutUint32(ew.Scratch[0:4], v)
	return ew.Write(ew.Scratch[0:4])
}

func (ew *EncodingWriter) WriteUint64(v uint64) error {
	binary.LittleEndian.PutUint64(ew.Scratch[0:8], v)
	return ew.Write(ew.Scratch[0:8])
}

// List serialization.
// If fixedElemSize == 0, then items are considered dynamic length, and will be encoded with offsets.
func (ew *EncodingWriter) List(item func(i uint64) Serializable, fixedElemSize uint64, length uint64) error {
	if fixedElemSize == 0 {
		prevOffset := 4 * length
		prevSize := uint64(0)
		// write offsets
		for i := uint64(0); i < length; i++ {
			size := item(i).ByteLength()
			offset, err := ew.WriteOffset(prevOffset, prevSize)
			if err != nil {
				return fmt.Errorf("failed to serialize list item %d: %v", i, err)
			}
			prevOffset = offset
			prevSize = size
		}
	}
	// write elements
	for i := uint64(0); i < length; i++ {
		if err := item(i).Serialize(ew); err != nil {
			return fmt.Errorf("failed to serialize list item %d: %v", i, err)
		}
	}
	return nil
}

// Vector serialization, works the same as encoding a List.
func (ew *EncodingWriter) Vector(item func(i uint64) Serializable, fixedElemSize uint64, length uint64) error {
	return ew.List(item, fixedElemSize, length)
}

func (ew *EncodingWriter) BitList(bits []byte) error {
	if len(bits) == 0 || bits[len(bits)-1] == 0 {
		return fmt.Errorf("missing delimiter bit, invalid bitlist")
	}
	return ew.Write(bits)
}

func (ew *EncodingWriter) BitVector(bits []byte) error {
	if len(bits) == 0 {
		return fmt.Errorf("bitvector must not be empty")
	}
	return ew.Write(bits)
}

func (ew *EncodingWriter) FixedLenContainer(fields ...Serializable) error {
	for i, f := range fields {
		if err := f.Serialize(ew); err != nil {
			return fmt.Errorf("failed to serialize fixed-length field %d: %v", i, err)
		}
	}
	return nil
}

// Container serialization. Fields with a non-zero .FixedLength() are considered fixed-length.
func (ew *EncodingWriter) Container(fields ...Serializable) error {
	fixedLen := uint64(0)
	isFixedLen := true
	for _, f := range fields {
		if fix := f.FixedLength(); fix != 0 {
			fixedLen += fix
		} else {
			// length of an offset
			fixedLen += 4
			isFixedLen = false
		}
	}
	// the previous offset, to calculate a new offset from, starting after the fixed data.
	prevOffset := fixedLen
	// span of the previous var-size element
	prevSize := uint64(0)
	for i, f := range fields {
		if f.FixedLength() != 0 {
			if err := f.Serialize(ew); err != nil {
				return fmt.Errorf("failed to serialize fixed-length field %d: %v", i, err)
			}
		} else {
			if offset, err := ew.WriteOffset(prevOffset, prevSize); err != nil {
				return err
			} else {
				prevOffset = offset
			}
			prevSize = f.ByteLength()
		}
	}
	// Only iterate over and write dynamic parts if we need to.
	if !isFixedLen {
		for i, f := range fields {
			if f.FixedLength() == 0 {
				if err := f.Serialize(ew); err != nil {
					return fmt.Errorf("failed to serialize dynamic-length field %d: %v", i, err)
				}
			}
		}
	}
	return nil
}

// Serialize a Union. The value
func (ew *EncodingWriter) Union(selector uint8, value Serializable) error {
	if err := ew.WriteByte(selector); err != nil {
		return fmt.Errorf("failed to write union selector (%d): %v", selector, err)
	}
	if value == nil {
		if selector != 0 {
			return fmt.Errorf("cannot serialize nil value, only 0 selector can be None, got %d", selector)
		}
		return nil
	}
	if err := value.Serialize(ew); err != nil {
		return fmt.Errorf("failed to write union value (selcector %v, value type %T): ", selector, value)
	}
	return nil
}
