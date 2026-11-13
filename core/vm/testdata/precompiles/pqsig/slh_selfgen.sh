#!/bin/bash
# Self-generated SLH-DSA-SHA2-128s vectors (pure, empty context), cross-verified:
# circl-signed -> verified by OpenSSL and circl; OpenSSL-signed -> verified by circl and OpenSSL.
set -u
O=${OPENSSL:-openssl} # OpenSSL >= 3.5
D=${1:?usage: slh_selfgen.sh <output-dir>}
HERE=$(cd "$(dirname "$0")" && pwd)
rm -rf "$D" && mkdir -p "$D" && D=$(cd "$D" && pwd) # absolute: the script changes directory below
cd "$HERE/slhgen" || exit 1
go run . sign "$D" || exit 1
spki() { # raw 32-byte pk file -> SPKI DER
  printf '\x30\x30\x30\x0b\x06\x09\x60\x86\x48\x01\x65\x03\x04\x03\x14\x03\x21\x00' > "$2"; cat "$1" >> "$2"
}
ossl_sign() { # len idx
  b=$(printf "%s/o_%05d_%d" "$D" "$1" "$2")
  "$O" genpkey -algorithm SLH-DSA-SHA2-128s -out "$b.key" 2>/dev/null
  "$O" pkey -in "$b.key" -pubout -outform DER -out "$b.der" 2>/dev/null
  tail -c 32 "$b.der" > "$b.pk"
  head -c "$1" /dev/urandom > "$b.msg"
  "$O" pkeyutl -sign -inkey "$b.key" -rawin -in "$b.msg" -out "$b.sig" 2>/dev/null
  rm -f "$b.key" "$b.der"
}
export -f ossl_sign; export O D
# OpenSSL CLI cannot read a 0-byte message, so its plan starts at 1 byte.
{ for i in 0 1 2 3 4 5 6 7; do for l in 1 32 33 1024; do echo "$l $i"; done; done; echo "65536 0"; echo "65536 1"; } | xargs -P 12 -n 2 bash -c 'ossl_sign "$0" "$1"'
# OpenSSL verifies everything except 0-byte messages (CLI limit; circl covers those).
okv=0; tot=0
for p in "$D"/*.pk; do
  b="${p%.pk}"
  [ -s "$b.msg" ] || continue
  spki "$p" "$b.tmp.der"; tot=$((tot+1))
  if "$O" pkeyutl -verify -pubin -inkey "$b.tmp.der" -keyform DER -rawin -in "$b.msg" -sigfile "$b.sig" >/dev/null 2>&1; then okv=$((okv+1)); else echo "FAIL openssl verify: $b"; fi
  rm -f "$b.tmp.der"
done
echo "openssl verified $okv/$tot"
go run . verify "$D"
