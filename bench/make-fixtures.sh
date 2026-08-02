#!/bin/bash
# Render the bench pages from the corpus. See bench/README.md for why they are
# not committed.
#
# DPI is part of each fixture's identity, not a global: the survey is rendered
# at 400 because at 200 every reader tried has misread its certificate number,
# and the ordinary pages stay at 200 because that is what production renders.
set -euo pipefail
CORPUS="${RAGLIT_BENCH_CORPUS:-$HOME/life/projects/ardley-v-brannock/documents}"
HERE="$(cd "$(dirname "$0")" && pwd)"
render() { # <probe> <pdf> <page> <dpi>
  local out="$HERE/probes/$1/_fixture"
  mkdir -p "$out"
  if [ ! -f "$2" ]; then echo "MISSING: $2" >&2; return 1; fi
  if ! pdftoppm -png -r "$4" -f "$3" -l "$3" -singlefile "$2" "$out/page"; then
    echo "FAILED to render $2 p$3 — is that page in the document?" >&2
    return 1
  fi
  echo "  $1  ← $(basename "$2") p$3 @${4}dpi"
}
echo "rendering bench fixtures from $CORPUS"
render ocr-survey-facts      "$CORPUS/records/202205230090-2022-halvor-ROS-disputed.pdf" 1 400
render ocr-survey-corners    "$CORPUS/records/202205230090-2022-halvor-ROS-disputed.pdf" 2 400
# The site plan, in the permit packet. The SAME drawing is also filed at
# records/2021-06-02-havern-access-permit-AC21-0044-with-1993-qcd.pdf p4 — two
# scans of one page, which is what makes an agreement check possible here
# without ground truth.
render ocr-drawing-dimensions "$CORPUS/correspondence/attachments/Re__24053_North_Northlea_Rd_Access_permit_-_Paul_Farley__1636_001.pdf" 4 200
render ocr-scanned-exhibit   "$CORPUS/evidence/icloud-2026-07-25/decoded/attachments/2021-05-24-PSA-OFFER-buyer-signed-30pg-MISNAMED-as-form22J__32945157.pdf" 28 200
echo "done"
