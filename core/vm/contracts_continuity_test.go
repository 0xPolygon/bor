// Copyright 2026 The go-ethereum Authors
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

package vm

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

type borPrecompileProfile string

const (
	borPrecompileProfilePrague       borPrecompileProfile = "prague"
	borPrecompileProfileMadhugiri    borPrecompileProfile = "madhugiri"
	borPrecompileProfileMadhugiriPro borPrecompileProfile = "madhugiriPro"
	borPrecompileProfileLisovo       borPrecompileProfile = "lisovo"
	borPrecompileProfileLisovoPro    borPrecompileProfile = "lisovoPro"
	borPrecompileProfileChicago      borPrecompileProfile = "chicago"
)

var borPrecompileProfiles = map[borPrecompileProfile]map[common.Address]string{
	borPrecompileProfilePrague: borPrecompileProfileExpectation(
		"MODEXP/eip2565=true/eip7823=false/eip7883=false",
		false,
		"P256VERIFY/eip7951=false",
		false,
	),
	borPrecompileProfileMadhugiri: borPrecompileProfileExpectation(
		"MODEXP/eip2565=true/eip7823=true/eip7883=true",
		true,
		"",
		false,
	),
	borPrecompileProfileMadhugiriPro: borPrecompileProfileExpectation(
		"MODEXP/eip2565=true/eip7823=true/eip7883=true",
		true,
		"P256VERIFY/eip7951=false",
		false,
	),
	borPrecompileProfileLisovo: borPrecompileProfileExpectation(
		"MODEXP/eip2565=true/eip7823=true/eip7883=true",
		true,
		"P256VERIFY/eip7951=true",
		false,
	),
	borPrecompileProfileLisovoPro: borPrecompileProfileExpectation(
		"MODEXP/eip2565=true/eip7823=true/eip7883=true",
		false,
		"P256VERIFY/eip7951=true",
		false,
	),
	borPrecompileProfileChicago: borPrecompileProfileExpectation(
		"MODEXP/eip2565=true/eip7823=true/eip7883=true",
		false,
		"P256VERIFY/eip7951=true",
		true,
	),
}

// TestBorHardforkPrecompileContinuityProfiles is the hardfork precompile
// continuity matrix. If a Bor VM hardfork changes active precompiles, update
// this matrix intentionally and include the reason in the PR.
func TestBorHardforkPrecompileContinuityProfiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		rules   params.Rules
		profile borPrecompileProfile
	}{
		{name: "Prague", rules: params.Rules{IsPrague: true}, profile: borPrecompileProfilePrague},
		{name: "Madhugiri", rules: params.Rules{IsMadhugiri: true}, profile: borPrecompileProfileMadhugiri},
		{name: "MadhugiriPro", rules: params.Rules{IsMadhugiriPro: true}, profile: borPrecompileProfileMadhugiriPro},
		{name: "Lisovo", rules: params.Rules{IsLisovo: true}, profile: borPrecompileProfileLisovo},
		{name: "LisovoPro", rules: params.Rules{IsLisovoPro: true}, profile: borPrecompileProfileLisovoPro},
		{name: "Chicago", rules: params.Rules{IsChicago: true}, profile: borPrecompileProfileChicago},
		// Hampi gates only the SSTORE committed-state read ordering, so it carries
		// Chicago's profile unchanged. At a post-Hampi block IsChicago is also set.
		{name: "Hampi", rules: params.Rules{IsChicago: true, IsHampi: true}, profile: borPrecompileProfileChicago},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertBorPrecompileProfile(t, tc.name, ActivePrecompiledContracts(tc.rules), tc.profile)
		})
	}
}

func TestBorHardforkPrecompileContinuityAtNetworkBoundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		network       string
		config        *params.ChainConfig
		fork          string
		block         *big.Int
		beforeProfile borPrecompileProfile
		atProfile     borPrecompileProfile
		afterProfile  borPrecompileProfile
	}{
		{
			network:       "amoy",
			config:        params.AmoyChainConfig,
			fork:          "Madhugiri",
			block:         params.AmoyChainConfig.Bor.MadhugiriBlock,
			beforeProfile: borPrecompileProfilePrague,
			atProfile:     borPrecompileProfileMadhugiri,
			afterProfile:  borPrecompileProfileMadhugiri,
		},
		{
			network:       "amoy",
			config:        params.AmoyChainConfig,
			fork:          "MadhugiriPro",
			block:         params.AmoyChainConfig.Bor.MadhugiriProBlock,
			beforeProfile: borPrecompileProfileMadhugiri,
			atProfile:     borPrecompileProfileMadhugiriPro,
			afterProfile:  borPrecompileProfileMadhugiriPro,
		},
		{
			network:       "amoy",
			config:        params.AmoyChainConfig,
			fork:          "Lisovo",
			block:         params.AmoyChainConfig.Bor.LisovoBlock,
			beforeProfile: borPrecompileProfileMadhugiriPro,
			atProfile:     borPrecompileProfileLisovo,
			afterProfile:  borPrecompileProfileLisovo,
		},
		{
			network:       "amoy",
			config:        params.AmoyChainConfig,
			fork:          "LisovoPro",
			block:         params.AmoyChainConfig.Bor.LisovoProBlock,
			beforeProfile: borPrecompileProfileLisovo,
			atProfile:     borPrecompileProfileLisovoPro,
			afterProfile:  borPrecompileProfileLisovoPro,
		},
		{
			network:       "amoy",
			config:        params.AmoyChainConfig,
			fork:          "Chicago",
			block:         params.AmoyChainConfig.Bor.ChicagoBlock,
			beforeProfile: borPrecompileProfileLisovoPro,
			atProfile:     borPrecompileProfileChicago,
			afterProfile:  borPrecompileProfileChicago,
		},
		// Mainnet activated Madhugiri/MadhugiriPro and Lisovo/LisovoPro at
		// the same blocks; only the Pro boundary is observable there.
		{
			network:       "mainnet",
			config:        params.BorMainnetChainConfig,
			fork:          "MadhugiriPro",
			block:         params.BorMainnetChainConfig.Bor.MadhugiriProBlock,
			beforeProfile: borPrecompileProfilePrague,
			atProfile:     borPrecompileProfileMadhugiriPro,
			afterProfile:  borPrecompileProfileMadhugiriPro,
		},
		{
			network:       "mainnet",
			config:        params.BorMainnetChainConfig,
			fork:          "LisovoPro",
			block:         params.BorMainnetChainConfig.Bor.LisovoProBlock,
			beforeProfile: borPrecompileProfileMadhugiriPro,
			atProfile:     borPrecompileProfileLisovoPro,
			afterProfile:  borPrecompileProfileLisovoPro,
		},
		{
			network:       "mainnet",
			config:        params.BorMainnetChainConfig,
			fork:          "Chicago",
			block:         params.BorMainnetChainConfig.Bor.ChicagoBlock,
			beforeProfile: borPrecompileProfileLisovoPro,
			atProfile:     borPrecompileProfileChicago,
			afterProfile:  borPrecompileProfileChicago,
		},
	}

	for _, tc := range cases {
		t.Run(tc.network+"/"+tc.fork, func(t *testing.T) {
			if tc.block == nil {
				t.Fatalf("%s %s block is nil", tc.network, tc.fork)
			}
			if tc.block.Sign() == 0 {
				t.Fatalf("%s %s block is zero; boundary test requires a non-genesis fork", tc.network, tc.fork)
			}

			before := new(big.Int).Sub(tc.block, big.NewInt(1))
			after := new(big.Int).Add(tc.block, big.NewInt(1))

			assertBorPrecompileProfileAtBlock(t, tc.config, before, tc.network, tc.fork+"-1", tc.beforeProfile)
			assertBorPrecompileProfileAtBlock(t, tc.config, tc.block, tc.network, tc.fork, tc.atProfile)
			assertBorPrecompileProfileAtBlock(t, tc.config, after, tc.network, tc.fork+"+1", tc.afterProfile)
		})
	}
}

func borPrecompileProfileExpectation(modExp string, includeKZG bool, p256 string, pip88 bool) map[common.Address]string {
	expectation := map[common.Address]string{
		precompileAddress(0x01): "ECREC",
		precompileAddress(0x02): "SHA256",
		precompileAddress(0x03): "RIPEMD160",
		precompileAddress(0x04): "ID",
		precompileAddress(0x05): modExp,
		precompileAddress(0x06): fmt.Sprintf("BN254_ADD/pip88=%t", pip88),
		precompileAddress(0x07): fmt.Sprintf("BN254_MUL/pip88=%t", pip88),
		precompileAddress(0x08): fmt.Sprintf("BN254_PAIRING/pip88=%t", pip88),
		precompileAddress(0x09): fmt.Sprintf("BLAKE2F/pip88=%t", pip88),
		precompileAddress(0x0b): fmt.Sprintf("BLS12_G1ADD/pip88=%t", pip88),
		precompileAddress(0x0c): fmt.Sprintf("BLS12_G1MSM/pip88=%t", pip88),
		precompileAddress(0x0d): fmt.Sprintf("BLS12_G2ADD/pip88=%t", pip88),
		precompileAddress(0x0e): fmt.Sprintf("BLS12_G2MSM/pip88=%t", pip88),
		precompileAddress(0x0f): fmt.Sprintf("BLS12_PAIRING_CHECK/pip88=%t", pip88),
		precompileAddress(0x10): fmt.Sprintf("BLS12_MAP_FP_TO_G1/pip88=%t", pip88),
		precompileAddress(0x11): fmt.Sprintf("BLS12_MAP_FP2_TO_G2/pip88=%t", pip88),
	}
	if includeKZG {
		expectation[precompileAddress(0x0a)] = "KZG_POINT_EVALUATION"
	}
	if p256 != "" {
		expectation[common.BytesToAddress([]byte{0x01, 0x00})] = p256
	}
	return expectation
}

func assertBorPrecompileProfileAtBlock(t *testing.T, config *params.ChainConfig, block *big.Int, network, label string, profile borPrecompileProfile) {
	t.Helper()

	rules := config.Rules(block, true, 0)
	assertBorPrecompileProfile(t, network+" "+label+" #"+block.String(), ActivePrecompiledContracts(rules), profile)
}

func assertBorPrecompileProfile(t *testing.T, label string, contracts PrecompiledContracts, profile borPrecompileProfile) {
	t.Helper()

	want, ok := borPrecompileProfiles[profile]
	if !ok {
		t.Fatalf("missing precompile profile expectation for %q", profile)
	}
	got := describePrecompileSet(t, contracts)

	if len(got) != len(want) {
		t.Errorf("%s: precompile count mismatch: got %d, want %d", label, len(got), len(want))
	}
	for addr, wantDescriptor := range want {
		gotDescriptor, ok := got[addr]
		if !ok {
			t.Errorf("%s: missing precompile %s (%s)", label, addr.Hex(), wantDescriptor)
			continue
		}
		if gotDescriptor != wantDescriptor {
			t.Errorf("%s: precompile %s mismatch: got %s, want %s", label, addr.Hex(), gotDescriptor, wantDescriptor)
		}
	}
	for addr, gotDescriptor := range got {
		if _, ok := want[addr]; !ok {
			t.Errorf("%s: unexpected precompile %s (%s)", label, addr.Hex(), gotDescriptor)
		}
	}
}

func describePrecompileSet(t *testing.T, contracts PrecompiledContracts) map[common.Address]string {
	t.Helper()

	descriptors := make(map[common.Address]string, len(contracts))
	for addr, contract := range contracts {
		descriptors[addr] = describePrecompile(t, contract)
	}
	return descriptors
}

func describePrecompile(t *testing.T, contract PrecompiledContract) string {
	t.Helper()

	switch c := contract.(type) {
	case *ecrecover:
		return c.Name()
	case *sha256hash:
		return c.Name()
	case *ripemd160hash:
		return c.Name()
	case *dataCopy:
		return c.Name()
	case *bigModExp:
		return fmt.Sprintf("%s/eip2565=%t/eip7823=%t/eip7883=%t", c.Name(), c.eip2565, c.eip7823, c.eip7883)
	case *bn256AddIstanbul:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bn256ScalarMulIstanbul:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bn256PairingIstanbul:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *blake2F:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *kzgPointEvaluation:
		return c.Name()
	case *bls12381G1Add:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bls12381G1MultiExp:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bls12381G2Add:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bls12381G2MultiExp:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bls12381Pairing:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bls12381MapG1:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *bls12381MapG2:
		return fmt.Sprintf("%s/pip88=%t", c.Name(), c.pip88)
	case *p256Verify:
		return fmt.Sprintf("%s/eip7951=%t", c.Name(), c.eip7951)
	default:
		t.Fatalf("unhandled precompile type %T (%s); extend describePrecompile before relying on the continuity matrix", contract, contract.Name())
		return ""
	}
}

func precompileAddress(addr byte) common.Address {
	return common.BytesToAddress([]byte{addr})
}
