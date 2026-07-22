#!/bin/sh
set -eu

query_id=6515125
limit=5000
start_page=0
max_pages=0
resume=0
output_dir=
temp_file=
inventory_temp=

usage() {
  echo "usage: download_pages.sh --query-id ID --output-dir DIR [--limit N] [--start-page N] [--max-pages N] [--resume]" >&2
}

die() {
  echo "download_pages.sh: $1" >&2
  exit 1
}

require_value() {
  [ "$#" -ge 2 ] || {
    usage
    die "missing value for $1"
  }
  case $2 in
    --*)
      usage
      die "missing value for $1"
      ;;
  esac
}

is_positive_integer() {
  case $1 in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$1" -gt 0 ] 2>/dev/null
}

is_nonnegative_integer() {
  case $1 in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$1" -ge 0 ] 2>/dev/null
}

cleanup() {
  if [ -n "$temp_file" ]; then
    rm -f -- "$temp_file"
  fi
  if [ -n "$inventory_temp" ]; then
    rm -f -- "$inventory_temp"
  fi
}

trap cleanup 0
trap 'exit 1' HUP INT TERM

while [ "$#" -gt 0 ]; do
  case $1 in
    --query-id)
      require_value "$@"
      query_id=$2
      shift 2
      ;;
    --output-dir)
      require_value "$@"
      output_dir=$2
      shift 2
      ;;
    --limit)
      require_value "$@"
      limit=$2
      shift 2
      ;;
    --start-page)
      require_value "$@"
      start_page=$2
      shift 2
      ;;
    --max-pages)
      require_value "$@"
      max_pages=$2
      shift 2
      ;;
    --resume)
      resume=1
      shift
      ;;
    *)
      usage
      die "unknown argument: $1"
      ;;
  esac
done

is_positive_integer "$query_id" || die "query id must be a positive integer"
is_positive_integer "$limit" || die "limit must be a positive integer"
is_nonnegative_integer "$start_page" || die "start page must be a non-negative integer"
is_nonnegative_integer "$max_pages" || die "max pages must be a non-negative integer"
[ -n "$output_dir" ] || die "--output-dir is required"
[ -n "${DUNE_API_KEY:-}" ] || die "DUNE_API_KEY is required"

mkdir -p -- "$output_dir"
[ -d "$output_dir" ] || die "output path is not a directory"

page=$start_page
attempted=0
while [ "$max_pages" -eq 0 ] || [ "$attempted" -lt "$max_pages" ]; do
  page_name=$(printf '%s_%04d.csv' "$query_id" "$page")
  page_path=$output_dir/$page_name
  existing=0
  if [ -s "$page_path" ]; then
    existing=1
    [ "$resume" -eq 1 ] || die "page already exists: $page_name"
  fi

  temp_file=$(mktemp "$output_dir/.dune-page.XXXXXX")
  offset=$((page * limit))
  url="https://api.dune.com/api/v1/query/${query_id}/results/csv?limit=${limit}&offset=${offset}"
  if ! curl --fail --silent --show-error --location --retry 5 \
    --header "x-dune-api-key: $DUNE_API_KEY" \
    --output "$temp_file" \
    "$url"; then
    die "Dune page request failed"
  fi
  attempted=$((attempted + 1))

  has_data=1
  if [ ! -s "$temp_file" ] || ! awk 'NR > 1 { exit 0 } END { if (NR <= 1) exit 1 }' "$temp_file"; then
    has_data=0
  fi

  if [ "$existing" -eq 1 ]; then
    if ! cmp -s -- "$page_path" "$temp_file"; then
      die "downloaded page differs from existing page: $page_name"
    fi
    rm -f -- "$temp_file"
    temp_file=
    if [ "$has_data" -eq 0 ]; then
      rm -f -- "$page_path"
      break
    fi
  elif [ "$has_data" -eq 0 ]; then
    rm -f -- "$temp_file"
    temp_file=
    break
  else
    mv -- "$temp_file" "$page_path"
    temp_file=
  fi

  page=$((page + 1))
done

inventory_path=${output_dir}.sha256
inventory_temp=$(mktemp "${inventory_path}.tmp.XXXXXX")
LC_ALL=C
export LC_ALL
for page_path in "$output_dir"/*.csv; do
  [ -f "$page_path" ] || continue
  page_name=${page_path##*/}
  digest=$(sha256sum "$page_path" | awk '{print $1}')
  printf '%s  %s\n' "$digest" "$page_name" >>"$inventory_temp"
done
mv -- "$inventory_temp" "$inventory_path"
inventory_temp=
