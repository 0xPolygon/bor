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
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
)

// Compression metrics
var (
	// Counters for operations
	compressionCount   = metrics.NewRegisteredCounter("witness/compression/count", nil)
	uncompressedCount  = metrics.NewRegisteredCounter("witness/uncompressed/count", nil)
	decompressionCount = metrics.NewRegisteredCounter("witness/decompression/count", nil)

	// Gauges for current values
	compressionRatio    = metrics.NewRegisteredGauge("witness/compression/ratio", nil)
	totalOriginalSize   = metrics.NewRegisteredGauge("witness/compression/original_size", nil)
	totalCompressedSize = metrics.NewRegisteredGauge("witness/compression/compressed_size", nil)
	spaceSavedBytes     = metrics.NewRegisteredGauge("witness/compression/space_saved", nil)

	// Timers for timing operations
	compressionTimer   = metrics.NewRegisteredTimer("witness/compression/timer", nil)
	decompressionTimer = metrics.NewRegisteredTimer("witness/decompression/timer", nil)

	// Meters for rates
	// compressionRate   = metrics.NewRegisteredMeter("witness/compression/rate", nil)
	// decompressionRate = metrics.NewRegisteredMeter("witness/decompression/rate", nil)
)

// CompressionStats returns current compression statistics
func CompressionStats() map[string]interface{} {
	compressed := compressionCount.Snapshot().Count()
	uncompressed := uncompressedCount.Snapshot().Count()
	total := compressed + uncompressed
	decompressed := decompressionCount.Snapshot().Count()

	var avgRatio float64
	if compressed > 0 {
		avgRatio = float64(compressionRatio.Snapshot().Value()) / float64(compressed)
	}

	var avgCompressionTime float64
	var totalCompressionTimeMs float64
	compressionTimerSnapshot := compressionTimer.Snapshot()
	if compressionTimerSnapshot.Count() > 0 {
		avgCompressionTime = float64(compressionTimerSnapshot.Mean()) / 1e6    // Convert to milliseconds
		totalCompressionTimeMs = float64(compressionTimerSnapshot.Sum()) / 1e6 // Total time in milliseconds
	}

	var avgDecompressionTime float64
	var totalDecompressionTimeMs float64
	decompressionTimerSnapshot := decompressionTimer.Snapshot()
	if decompressionTimerSnapshot.Count() > 0 {
		avgDecompressionTime = float64(decompressionTimerSnapshot.Mean()) / 1e6    // Convert to milliseconds
		totalDecompressionTimeMs = float64(decompressionTimerSnapshot.Sum()) / 1e6 // Total time in milliseconds
	}

	var compressionRateBps float64
	if compressionTimerSnapshot.Count() > 0 && compressionTimerSnapshot.Mean() > 0 {
		compressionRateBps = float64(totalCompressedSize.Snapshot().Value()) / compressionTimerSnapshot.Mean() * 1e9 // bytes per second
	}

	var decompressionRateBps float64
	if decompressionTimerSnapshot.Count() > 0 && decompressionTimerSnapshot.Mean() > 0 {
		decompressionRateBps = float64(totalCompressedSize.Snapshot().Value()) / decompressionTimerSnapshot.Mean() * 1e9 // bytes per second
	}

	return map[string]interface{}{
		"compression_count":     compressed,
		"uncompressed_count":    uncompressed,
		"total_witnesses":       total,
		"compression_ratio":     avgRatio,
		"total_original_size":   totalOriginalSize.Snapshot().Value(),
		"total_compressed_size": totalCompressedSize.Snapshot().Value(),
		"space_saved_bytes":     spaceSavedBytes.Snapshot().Value(),

		// Compression timing and rate metrics
		"total_compression_time_ms": totalCompressionTimeMs,
		"avg_compression_time_ms":   avgCompressionTime,
		"total_compression_size":    totalCompressedSize.Snapshot().Value(),
		"compression_rate_bps":      compressionRateBps,

		// Decompression metrics
		"decompression_count":         decompressed,
		"total_decompression_time_ms": totalDecompressionTimeMs,
		"avg_decompression_time_ms":   avgDecompressionTime,
		"total_decompression_size":    totalCompressedSize.Snapshot().Value(),
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
		CompressionLevel: gzip.BestSpeed,
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
	totalOriginalSize.Inc(int64(originalSize))

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
			compressionCount.Inc(1)
			totalCompressedSize.Inc(int64(len(compressedData)))
			compressionTimer.Update(time.Duration(compressionTime))
			ratio := int64(float64(len(compressedData)) / float64(originalSize) * 100)
			compressionRatio.Update(ratio)

			// Update space saved
			spaceSaved := int64(originalSize) - int64(len(compressedData))
			spaceSavedBytes.Inc(spaceSaved)

			// Write compression marker and compressed data
			if _, err := wr.Write([]byte{0x01}); err != nil {
				return err
			}
			_, err = wr.Write(compressedData)
			return err
		}
	}

	// Track uncompressed metrics
	uncompressedCount.Inc(1)
	totalCompressedSize.Inc(int64(originalSize))

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
		decompressionCount.Inc(1)
		decompressionTimer.Update(time.Duration(decompressionTime))
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
