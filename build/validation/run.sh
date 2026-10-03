#!/usr/bin/env bash
set -euo pipefail
cd /src
if [[ "${1:-verify}" == preview ]]; then
  exec node build/validation/preview.cjs
fi
mkdir -p /artifacts/screenshots
cp /out/magpie-windows-amd64.exe /artifacts/
sha256sum /artifacts/magpie-windows-amd64.exe > /artifacts/SHA256SUMS.txt
# Tests use the image's modules and a Docker build-cache volume; neither
# dependencies nor generated files are written into the source checkout.
make test 2>&1 | tee /artifacts/go-tests.log
ARTIFACT_DIR=/artifacts/screenshots node --test --test-concurrency=1 \
  internal/gui/tests/balance-fix.test.cjs \
  internal/gui/tests/balance-templates.test.cjs \
  internal/gui/tests/balance-parts.test.cjs 2>&1 | tee /artifacts/browser-tests.log
ARTIFACT_DIR=/artifacts/screenshots node --test --test-concurrency=1 \
  build/validation/balance-e2e.test.cjs 2>&1 | tee /artifacts/e2e-tests.log
