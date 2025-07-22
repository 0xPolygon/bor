// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package stateless

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// Compression metrics
var (
	compressionRatio    int64
	compressionCount    int64
	uncompressedCount   int64
	totalOriginalSize   int64
	totalCompressedSize int64

	// Compression timing and rate metrics
	totalCompressionTime int64 // nanoseconds
	totalCompressionSize int64 // total size of compressed data
	compressionRate      int64 // bytes per second

	// Decompression metrics
	decompressionCount     int64
	totalDecompressionTime int64 // nanoseconds
	totalDecompressionSize int64 // total size of decompressed data
	decompressionRate      int64 // bytes per second
)

// CompressionStats returns current compression statistics
func CompressionStats() map[string]interface{} {
	compressed := atomic.LoadInt64(&compressionCount)
	uncompressed := atomic.LoadInt64(&uncompressedCount)
	total := compressed + uncompressed
	decompressed := atomic.LoadInt64(&decompressionCount)

	var avgRatio float64
	if compressed > 0 {
		avgRatio = float64(atomic.LoadInt64(&compressionRatio)) / float64(compressed)
	}

	var avgCompressionTime float64
	if compressed > 0 {
		avgCompressionTime = float64(atomic.LoadInt64(&totalCompressionTime)) / float64(compressed) / 1e6 // Convert to milliseconds
	}

	var avgDecompressionTime float64
	if decompressed > 0 {
		avgDecompressionTime = float64(atomic.LoadInt64(&totalDecompressionTime)) / float64(decompressed) / 1e6 // Convert to milliseconds
	}

	var compressionRateBps float64
	if atomic.LoadInt64(&totalCompressionTime) > 0 {
		compressionRateBps = float64(atomic.LoadInt64(&totalCompressionSize)) / float64(atomic.LoadInt64(&totalCompressionTime)) * 1e9 // bytes per second
	}

	var decompressionRateBps float64
	if atomic.LoadInt64(&totalDecompressionTime) > 0 {
		decompressionRateBps = float64(atomic.LoadInt64(&totalDecompressionSize)) / float64(atomic.LoadInt64(&totalDecompressionTime)) * 1e9 // bytes per second
	}

	return map[string]interface{}{
		"compression_count":     compressed,
		"uncompressed_count":    uncompressed,
		"total_witnesses":       total,
		"compression_ratio":     avgRatio,
		"total_original_size":   atomic.LoadInt64(&totalOriginalSize),
		"total_compressed_size": atomic.LoadInt64(&totalCompressedSize),
		"space_saved_bytes":     atomic.LoadInt64(&totalOriginalSize) - atomic.LoadInt64(&totalCompressedSize),

		// Compression timing and rate metrics
		"total_compression_time_ms": float64(atomic.LoadInt64(&totalCompressionTime)) / 1e6,
		"avg_compression_time_ms":   avgCompressionTime,
		"total_compression_size":    atomic.LoadInt64(&totalCompressionSize),
		"compression_rate_bps":      compressionRateBps,

		// Decompression metrics
		"decompression_count":         decompressed,
		"total_decompression_time_ms": float64(atomic.LoadInt64(&totalDecompressionTime)) / 1e6,
		"avg_decompression_time_ms":   avgDecompressionTime,
		"total_decompression_size":    atomic.LoadInt64(&totalDecompressionSize),
		"decompression_rate_bps":      decompressionRateBps,
	}
}

// Compression threshold in bytes. Only compress if witness is larger than this.
// 1MB is the minimum size for compression to be worthwhile
const compressionThreshold = 1 * 1024 * 1024

// CompressionConfig holds configuration for witness compression
type CompressionConfig struct {
	Enabled          bool // Enable/disable compression
	Threshold        int  // Threshold in bytes. Only compress if witness is larger than this.
	CompressionLevel int  // Gzip compression level (1-9)
	UseDeduplication bool // Enable witness optimization
}

// DefaultCompressionConfig returns the default compression configuration
func DefaultCompressionConfig() *CompressionConfig {
	return &CompressionConfig{
		Enabled:          true,
		Threshold:        compressionThreshold,
		CompressionLevel: gzip.BestCompression,
		UseDeduplication: true,
	}
}

// Global compression configuration
var globalCompressionConfig = DefaultCompressionConfig()

// SetCompressionConfig sets the global compression configuration
func SetCompressionConfig(config *CompressionConfig) {
	globalCompressionConfig = config
}

// GetCompressionConfig returns the current compression configuration
func GetCompressionConfig() *CompressionConfig {
	return globalCompressionConfig
}

// toExtWitness converts our internal witness representation to the consensus one.
func (w *Witness) toExtWitness() *extWitness {
	w.lock.RLock()
	defer w.lock.RUnlock()

	ext := &extWitness{
		Context: w.context,
		Headers: w.Headers,
	}
	ext.State = make([][]byte, 0, len(w.State))
	for node := range w.State {
		ext.State = append(ext.State, []byte(node))
	}
	return ext
}

// fromExtWitness converts the consensus witness format into our internal one.
func (w *Witness) fromExtWitness(ext *extWitness) error {
	w.context = ext.Context
	w.Headers = ext.Headers
	w.State = make(map[string]struct{}, len(ext.State))
	for _, node := range ext.State {
		w.State[string(node)] = struct{}{}
	}
	return nil
}

// EncodeRLP serializes a witness as RLP.
func (w *Witness) EncodeRLP(wr io.Writer) error {
	// Optimize witness if deduplication is enabled
	if globalCompressionConfig.UseDeduplication {
		w.Optimize()
	}

	// Use the original RLP encoding
	return rlp.Encode(wr, w.toExtWitness())
}

// DecodeRLP decodes a witness from RLP.
func (w *Witness) DecodeRLP(s *rlp.Stream) error {
	var ext extWitness
	if err := s.Decode(&ext); err != nil {
		return err
	}
	return w.fromExtWitness(&ext)
}

// EncodeCompressed serializes a witness with optional compression.
func (w *Witness) EncodeCompressed(wr io.Writer) error {
	// First encode to RLP
	var rlpBuf bytes.Buffer
	if err := w.EncodeRLP(&rlpBuf); err != nil {
		return err
	}

	rlpData := rlpBuf.Bytes()
	originalSize := len(rlpData)

	// Track original size
	atomic.AddInt64(&totalOriginalSize, int64(originalSize))

	// Only compress if enabled and the data is large enough to benefit from compression
	if globalCompressionConfig.Enabled && len(rlpData) > globalCompressionConfig.Threshold {
		// Start timing compression
		startTime := time.Now()

		// Compress the RLP data
		var compressedBuf bytes.Buffer
		gw, err := gzip.NewWriterLevel(&compressedBuf, globalCompressionConfig.CompressionLevel)
		if err != nil {
			return err
		}

		if _, err := gw.Write(rlpData); err != nil {
			return err
		}

		if err := gw.Close(); err != nil {
			return err
		}

		compressedData := compressedBuf.Bytes()

		// Calculate compression time
		compressionTime := time.Since(startTime).Nanoseconds()

		// Only use compression if it actually reduces size
		if len(compressedData) < len(rlpData) {
			// Track compression metrics
			atomic.AddInt64(&compressionCount, 1)
			atomic.AddInt64(&totalCompressedSize, int64(len(compressedData)))
			atomic.AddInt64(&totalCompressionTime, compressionTime)
			atomic.AddInt64(&totalCompressionSize, int64(len(compressedData)))
			ratio := int64(float64(len(compressedData)) / float64(originalSize) * 100)
			atomic.AddInt64(&compressionRatio, ratio)

			// Write compression marker and compressed data
			if _, err := wr.Write([]byte{0x01}); err != nil {
				return err
			}
			_, err = wr.Write(compressedData)
			return err
		}
	}

	// Track uncompressed metrics
	atomic.AddInt64(&uncompressedCount, 1)
	atomic.AddInt64(&totalCompressedSize, int64(originalSize))

	// Write uncompressed marker and original RLP data
	if _, err := wr.Write([]byte{0x00}); err != nil {
		return err
	}
	_, err := wr.Write(rlpData)
	return err
}

// DecodeCompressed decodes a witness from compressed format.
func (w *Witness) DecodeCompressed(data []byte) error {
	if len(data) == 0 {
		return errors.New("empty data")
	}

	// Check compression marker
	compressed := data[0] == 0x01
	witnessData := data[1:]

	var rlpData []byte
	if compressed {
		// Start timing decompression
		startTime := time.Now()

		// Decompress
		gr, err := gzip.NewReader(bytes.NewReader(witnessData))
		if err != nil {
			return err
		}
		defer gr.Close()

		var decompressedBuf bytes.Buffer
		if _, err := io.Copy(&decompressedBuf, gr); err != nil {
			return err
		}
		rlpData = decompressedBuf.Bytes()

		// Calculate decompression time and track metrics
		decompressionTime := time.Since(startTime).Nanoseconds()
		atomic.AddInt64(&decompressionCount, 1)
		atomic.AddInt64(&totalDecompressionTime, decompressionTime)
		atomic.AddInt64(&totalDecompressionSize, int64(len(rlpData)))
	} else {
		rlpData = witnessData
	}

	// Decode the RLP data
	var ext extWitness
	if err := rlp.DecodeBytes(rlpData, &ext); err != nil {
		return err
	}

	return w.fromExtWitness(&ext)
}

// extWitness is a witness RLP encoding for transferring across clients.
type extWitness struct {
	Context *types.Header
	Headers []*types.Header
	State   [][]byte
}
