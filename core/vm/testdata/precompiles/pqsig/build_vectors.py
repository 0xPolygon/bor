#!/usr/bin/env python3
"""Build Bor precompile test vectors (core/vm/testdata/precompiles/*.json) for
the ML-DSA-65 and SLH-DSA-SHA2-128s verify precompiles.

Input layout of both precompiles: pk || sig || msg (pure mode, empty context).
Output: 32 bytes, 1 = valid, 0 = anything else.

Sources (pinned):
  Wycheproof   C2SP/wycheproof @ d17cc89fa5c8f4f4d378b5385ebf75da5d0926c9 (testvectors_v1)
  ACVP         usnistgov/ACVP-Server @ 975de31eb83d87039ec88934fdc47d8c312b892d (ctx="" external/pure cases)
  Apple SE     research/pq-signatures/se-vectors/apple-se-vectors.json (CryptoKit, 2026-10-22)
  self-made    research/pq-signatures/vectors/slh_selfgen (circl v1.6.5 + OpenSSL 3.6.1, cross-verified)

Usage: python3 -I build_bor_vectors.py <vectors-dir> <slhgen-dir> <apple-json> <out-dir>
"""
import json
import os
import sys

MLDSA65_PK, MLDSA65_SIG = 1952, 3309
SLH_PK, SLH_SIG = 32, 7856
# Provisional prices (M4 Pro, see phase4-implementation-design.md D3).
MLDSA65_BASE, MLDSA_WORD = 17000, 6
SLH_BASE, SLH_WORD = 41000, 12

ONE = "00" * 31 + "01"
ZERO = "00" * 32


def gas(inp_len, fixed, base, word):
    msg = inp_len - fixed
    if msg <= 0:
        return base
    return base + (msg + 31) // 32 * word


def case(name, pk, sig, msg, valid, fixed, base, word, bench=False):
    inp = pk + sig + msg
    return {
        "Input": inp.hex(),
        "Expected": ONE if valid else ZERO,
        "Name": name,
        "Gas": gas(len(inp), fixed, base, word),
        "NoBenchmark": not bench,
    }


def mldsa_cases(vec, apple):
    out = []
    ml = lambda n, pk, sig, msg, v, b=False: case(n, pk, sig, msg, v, MLDSA65_PK + MLDSA65_SIG, MLDSA65_BASE, MLDSA_WORD, b)
    # Wycheproof verify: every case. A non-empty ctx can't be expressed by the
    # precompile, so those cases must give 0 even when the source says valid.
    w = json.load(open(os.path.join(vec, "wycheproof/mldsa_65_verify_test.json")))
    for g in w["testGroups"]:
        pk = bytes.fromhex(g["publicKey"])
        for t in g["tests"]:
            ctx = t.get("ctx", "")
            valid = t["result"] == "valid" and ctx == "" and len(pk) == MLDSA65_PK and len(t["sig"]) // 2 == MLDSA65_SIG
            out.append(ml(f"wycheproof-verify-{t['tcId']}" + ("-ctx" if ctx else ""), pk, bytes.fromhex(t["sig"]), bytes.fromhex(t["msg"]), valid))
    # Wycheproof sign files: valid signatures with an empty ctx and a real message.
    for f in ("mldsa_65_sign_seed_test.json", "mldsa_65_sign_noseed_test.json"):
        w = json.load(open(os.path.join(vec, "wycheproof", f)))
        tag = "seed" if "noseed" not in f else "noseed"
        for g in w["testGroups"]:
            if not g.get("publicKey"):  # private-key length tests only; nothing to verify
                continue
            pk = bytes.fromhex(g["publicKey"])
            for t in g["tests"]:
                if t["result"] != "valid" or t.get("ctx", "") != "" or "msg" not in t or "sig" not in t:
                    continue
                out.append(ml(f"wycheproof-sign-{tag}-{t['tcId']}", pk, bytes.fromhex(t["sig"]), bytes.fromhex(t["msg"]), True))
    # ACVP external/pure ctx="" (sigGen tc44 deterministic, tc216 hedged; sigVer tc39 modified message).
    for tag, valid in (("tg3_tc44", True), ("tg15_tc216", True)):
        d = os.path.join(vec, "ctx0/ml")
        out.append(acvp(ml, d, tag, MLDSA65_PK, valid))
    out.append(acvp(ml, os.path.join(vec, "ctx0/mlv"), "tg3_tc39", MLDSA65_PK, False))
    # Apple Secure Enclave (CryptoKit). The 1 MiB message is left out of the JSON (2 MiB of hex).
    for g in apple:
        if g["algorithm"] != "ML-DSA-65":
            continue
        pk = bytes.fromhex(g["publicKey"])
        for c in g["cases"]:
            if len(c["message"]) // 2 > 65536:
                continue
            out.append(ml(f"apple-se-{c['name']}", pk, bytes.fromhex(c["signature"]), bytes.fromhex(c["message"]),
                          c["expectEIP8355"], b=c["name"] in ("digest32", "msg_64KiB")))
    return out


def acvp(mk, d, tag, pklen, valid):
    pk = open(os.path.join(d, tag + ".pub.der"), "rb").read()[-pklen:]
    sig = open(os.path.join(d, tag + ".sig"), "rb").read()
    msg = open(os.path.join(d, tag + ".msg"), "rb").read()
    return mk(f"acvp-{tag}", pk, sig, msg, valid)


def slh_cases(vec, gen):
    out = []
    sl = lambda n, pk, sig, msg, v, b=False: case(n, pk, sig, msg, v, SLH_PK + SLH_SIG, SLH_BASE, SLH_WORD, b)
    for tag, valid in (("tg19_tc162", True), ("tg55_tc474", True)):
        out.append(acvp(sl, os.path.join(vec, "ctx0/slh"), tag, SLH_PK, valid))
    out.append(acvp(sl, os.path.join(vec, "ctx0/slhv"), "tg19_tc255", SLH_PK, False))
    for f in sorted(os.listdir(gen)):
        if not f.endswith(".pk"):
            continue
        b = os.path.join(gen, f[:-3])
        signer = "circl" if f.startswith("c_") else "openssl"
        pk, sig, msg = (open(b + e, "rb").read() for e in (".pk", ".sig", ".msg"))
        name = f"selfgen-{signer}-{f[2:-3]}"
        out.append(sl(name, pk, sig, msg, True, b=(f[2:-3] in ("00032_0", "65536_0"))))
    return out


def main():
    vec, gen, apple_path, outdir = sys.argv[1:5]
    apple = json.load(open(apple_path))
    for fname, cases in (("mldsa65Verify.json", mldsa_cases(vec, apple)), ("slhdsaSha2128sVerify.json", slh_cases(vec, gen))):
        names = [c["Name"] for c in cases]
        assert len(names) == len(set(names)), "duplicate names in " + fname
        json.dump(cases, open(os.path.join(outdir, fname), "w"), indent=1)
        valid = sum(c["Expected"] == ONE for c in cases)
        print(f"{fname}: {len(cases)} cases ({valid} valid, {len(cases) - valid} invalid)")


if __name__ == "__main__":
    main()
