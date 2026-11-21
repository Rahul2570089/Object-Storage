#!/usr/bin/env bash
# Run from the repo root after `make build`. Needs port 8080 free.
set -u
BIN=./build/storage-server
URL=http://localhost:8080

rm -rf storage /tmp/crash.bin
head -c 500000000 /dev/urandom > /tmp/crash.bin    # ~500 MB

$BIN & SERVER=$!
sleep 1
curl -s -X PUT $URL/buckets/test > /dev/null
curl -s --limit-rate 20M -T /tmp/crash.bin $URL/objects/test/big.bin > /dev/null &
CURL=$!
sleep 5
kill -9 $SERVER                      # simulate a crash mid-upload
wait $CURL 2>/dev/null

$BIN & SERVER=$!                     # restart: tmp cleanup runs on startup
sleep 1
code=$(curl -s -o /dev/null -w '%{http_code}' $URL/objects/test/big.bin)
leftover=$(ls storage/tmp | wc -l)
kill $SERVER 2>/dev/null

if [ "$code" = "404" ] && [ "$leftover" -eq 0 ]; then
  echo "PASS: no partial object visible, no temp files left"
else
  echo "FAIL: http=$code, temp files=$leftover"; exit 1
fi