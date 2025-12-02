package grpc_net_conn

import (
	"google.golang.org/protobuf/proto"
)

// Encoder encodes a byte slice to write into the destination proto.Message.
// You do not need to copy the slice; you may use it directly.
//
// You do not have to encode the full byte slice in one packet. You can
// choose to chunk your packets by returning 0 < n < len(p) and the
// Conn will repeatedly send subsequent messages by slicing into the
// byte slice.
type Encoder[T proto.Message] func(T, []byte) (int, error)

// Decoder is given a Response value and expects you to decode the
// response value into the byte slice given. You MUST decode up to
// len(p) if available.
//
// This should return the data slice directly from m. The length of this
// is used to determine if there is more data and the offset for the next
// read.
type Decoder[T proto.Message] func(m T, offset int, p []byte) ([]byte, error)

// SimpleEncoder is the easiest way to generate an Encoder for a proto.Message.
// You just give it a callback that gets the pointer to the byte slice field
// and a valid encoder will be generated.
//
// Example: given a structure that has a field "Data []byte", you could:
//
//	SimpleEncoder(func(msg proto.Message) *[]byte {
//	    return &msg.(*MyStruct).Data
//	})
func SimpleEncoder[T proto.Message](innerDataPtrGetter func(msg T) *[]byte) Encoder[T] {
	return func(msg T, p []byte) (int, error) {
		bytePtr := innerDataPtrGetter(msg)
		*bytePtr = p

		return len(p), nil
	}
}

// SimpleDecoder is the easiest way to generate a Decoder for a proto.Message.
// Provide a callback that gets the pointer to the byte slice field and a
// valid decoder will be generated.
func SimpleDecoder[T proto.Message](innerDataPtrGetter func(msg T) *[]byte) Decoder[T] {
	return func(msg T, offset int, p []byte) ([]byte, error) {
		bytePtr := innerDataPtrGetter(msg)
		copy(p, (*bytePtr)[offset:])

		return *bytePtr, nil
	}
}

// ChunkedEncoder ensures that data to encode is chunked at the proper size.
func ChunkedEncoder[T proto.Message](enc Encoder[T], size int) Encoder[T] {
	return func(msg T, p []byte) (int, error) {
		if len(p) > size {
			p = p[:size]
		}

		return enc(msg, p)
	}
}
