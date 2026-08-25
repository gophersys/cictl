#!/usr/bin/env bash
# Archive the reviewer's full event stream — every tool call, every file read,
# the cost and the turn count — to an S3-compatible object store.
#
# WHY NOT actions/upload-artifact. GitHub artifact storage is one quota for the
# whole organization. When it filled, `Failed to CreateArtifact: Artifact storage
# quota has been hit` failed the keep-the-run step on runs whose Review step had
# SUCCEEDED — measured on gophersys/.devcontainer run 32868094840 and
# gophersys/infrastructure run 32763725563, and five times across one weekend.
# One of those reds landed on a review that had APPROVED. A shared quota must
# never decide one repository's review verdict, and a red that does not mean what
# a reader thinks is how people learn to ignore red.
#
# WHY THIS STEP MAY FAIL THE JOB. The review comment is already posted by the
# time this runs, so the pull request carries its verdict either way. What is at
# stake here is the RECORD, and a lost record is a real failure: without the
# stream, diagnosing a wrong review means paying for another run. So a missing
# credential and a failed upload are both loud, and neither is a silent skip. The
# message says plainly that the review itself completed, so nobody reads this red
# as a verdict.
#
# EVERY GUARD IS PROVEN, on 1 line, ending in a `# guard:<name>` marker. See
# review/action_test.sh.
set -Eeuo pipefail
IFS=$'\n\t'

log() { printf '\033[0;36m[review-archive]\033[0m %s\n' "$*"; }
die() { printf '\033[0;31m[review-archive]\033[0m %s\n' "$*" >&2; exit 1; }

STORE="${REVIEW_STREAM_STORE:-minio}"
STREAM="${REVIEW_STREAM_FILE:-}"

# `none` is an explicit, visible opt-out declared in the caller's contract. It is
# not a fallback: an unknown value is refused rather than treated as none, so a
# typo cannot silently turn archiving off for a repository.
case "$STORE" in
  none)
    log "stream-store is 'none': this repository keeps no review stream, by declaration"
    exit 0
    ;;
  minio) : ;;
  *) die "stream-store must be 'minio' or 'none', got '$STORE'" ;; # guard:store
esac

require_env() { [ -n "${!1:-}" ] || die "$1 is empty. $2"; }

require_tools() {
  local missing=() c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || missing+=("$c")
  done
  [ "${#missing[@]}" -eq 0 ] || die "missing required tool(s): ${missing[*]}"
}

# curl signs the request itself with --aws-sigv4, so no second client has to be
# installed on the pool to reach an S3-compatible store.
require_tools curl # guard:tools

require_env REVIEW_STREAM_ENDPOINT "stream-endpoint is required when stream-store is minio. There is deliberately no default: an endpoint nobody verified reads as configuration and archives nothing." # guard:endpoint
require_env REVIEW_STREAM_BUCKET "stream-bucket is required when stream-store is minio." # guard:bucket
require_env REVIEW_STREAM_KEY "the object key is composed by the action from the repository, the pull request and the run; an empty one would overwrite another run's stream." # guard:key

# The credential is pool environment, exactly like the Claude token: the pool
# runs this action and nothing else, so the keys have one home and no caller
# passes them. Absent is a FAILURE and never a skip — a skip here is a green run
# that archived nothing while reporting that it did.
require_env MINIO_ACCESS_KEY "the arc-review pool supplies it. A missing credential must fail the archive, never skip it." # guard:credential
require_env MINIO_SECRET_KEY "the arc-review pool supplies it. A missing credential must fail the archive, never skip it." # guard:credential-secret

# review.sh refuses an empty or missing stream itself, and it does so BEFORE
# posting anything, so reaching this line with no file means the review died
# before it produced one event. That is worth naming rather than uploading zero
# bytes over the top of nothing.
[ -s "$STREAM" ] || die "there is no stream to archive at '${STREAM}': the review produced no events. The Review step above is already red in that case; this line only says why there is no record." # guard:stream

endpoint="${REVIEW_STREAM_ENDPOINT%/}"
url="${endpoint}/${REVIEW_STREAM_BUCKET}/${REVIEW_STREAM_KEY}"

log "archiving $(wc -c < "$STREAM" | tr -d ' ') bytes to ${REVIEW_STREAM_BUCKET}/${REVIEW_STREAM_KEY}"

# --fail-with-body so a 4xx/5xx is a non-zero exit AND the store's own error text
# reaches the log. A bare --fail hides the body, which is where MinIO says whether
# the bucket is missing or the signature is wrong.
# The region is a SigV4 formality: MinIO ignores it, and curl requires one.
curl --fail-with-body -sS -X PUT \
  --user "${MINIO_ACCESS_KEY}:${MINIO_SECRET_KEY}" \
  --aws-sigv4 "aws:amz:us-east-1:s3" \
  --header "Content-Type: application/x-ndjson" \
  --upload-file "$STREAM" \
  "$url" || die "the review completed, but its event stream could not be archived to ${url}. The verdict is on the pull request; only the record is lost." # guard:upload

log "archived: ${url}"
