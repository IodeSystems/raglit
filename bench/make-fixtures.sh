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
render ocr-survey-facts      "$CORPUS/records/201907220018-2019-dace-ROS-disputed.pdf" 1 400
render ocr-survey-corners    "$CORPUS/records/201907220018-2019-dace-ROS-disputed.pdf" 2 400
# The site plan, in the permit packet. The SAME drawing is also filed at
# records/2019-04-09-havern-access-permit-AC19-0044-with-1993-qcd.pdf p4 — two
# scans of one page, which is what makes an agreement check possible here
# without ground truth.
render ocr-drawing-dimensions "$CORPUS/correspondence/attachments/Re__1440_North_Northlea_Rd_Access_permit_-_Ivo_Larkin__1636_001.pdf" 4 200
render ocr-scanned-exhibit   "$CORPUS/evidence/icloud-2026-07-25/decoded/attachments/2019-04-09-PSA-OFFER-buyer-signed-30pg-MISNAMED-as-form22J__40118802.pdf" 28 200
# The 27x36.7in sheet the region descent exists for. 200 dpi, not 400: the
# encoder caps at 4000 tokens for an image, so above ~205 sq in the dpi buys
# nothing and only the crop does. This is the ONLY fixture that reaches the
# tiling path — a letter page is never flagged low-resolution.
render ocr-esize-survey      "$CORPUS/records/201503110045-2008-summit-record-of-survey-OPERATIVE.pdf" 1 200
echo "done"
