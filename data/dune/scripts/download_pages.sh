#!/bin/sh
# Requires GNU/Linux with curl, mktemp, sha256sum, and Python 3.
set -eu

query_id=6515125
limit=5000
start_page=0
max_pages=0
resume=0
output_dir=
temp_file=
inventory_temp=
header_file=
status_temp=

usage() {
  echo "usage: download_pages.sh --query-id ID --output-dir DIR [--limit N] [--start-page N] [--max-pages N] [--resume]" >&2
  echo "requires GNU/Linux with curl, mktemp, sha256sum, and python3" >&2
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
  if [ -n "$header_file" ]; then
    rm -f -- "$header_file"
  fi
  if [ -n "$status_temp" ]; then
    rm -f -- "$status_temp"
  fi
}

trap cleanup 0
trap 'exit 1' HUP INT TERM

for required_command in curl mktemp sha256sum python3; do
  command -v "$required_command" >/dev/null 2>&1 || \
    die "required command not found: $required_command"
done

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
[ "$limit" -le 1000000 ] || die "limit must not exceed 1000000"
[ "$start_page" -le 9999 ] || die "start page must not exceed 9999"
[ -n "$output_dir" ] || die "--output-dir is required"
[ -n "${DUNE_API_KEY:-}" ] || die "DUNE_API_KEY is required"
api_key=$DUNE_API_KEY
unset DUNE_API_KEY
case $api_key in
  *[!A-Za-z0-9._-]*)
    api_key=
    die "DUNE_API_KEY contains unsupported characters"
    ;;
esac

path_status=0
python3 - "$output_dir" <<'PY' || path_status=$?
import sys

path = sys.argv[1]
raise SystemExit(1 if "\r" in path or "\n" in path else 0)
PY
[ "$path_status" -eq 0 ] || die "output directory must not contain CR or LF"
mkdir -p -- "$output_dir"
[ -d "$output_dir" ] || die "output path is not a directory"
if ! output_dir=$(CDPATH= cd -- "$output_dir" && pwd -P); then
  die "could not normalize output directory"
fi
[ "$output_dir" != / ] || die "output directory must not be filesystem root"
header_file=$(mktemp "$output_dir/.dune-header.XXXXXX")
chmod 600 "$header_file"
printf 'x-dune-api-key: %s\n' "$api_key" >"$header_file"
api_key=

page=$start_page
attempted=0
while [ "$max_pages" -eq 0 ] || [ "$attempted" -lt "$max_pages" ]; do
  [ "$page" -le 9999 ] || die "page must not exceed 9999"
  page_name=$(printf '%s_%04d.csv' "$query_id" "$page")
  page_path=$output_dir/$page_name
  existing=0
  if [ -L "$page_path" ]; then
    die "page path must not be a symlink: $page_name"
  fi
  if [ -e "$page_path" ]; then
    existing=1
    [ "$resume" -eq 1 ] || die "page already exists: $page_name"
  fi

  temp_file=$(mktemp "$output_dir/.dune-page.XXXXXX")
  status_temp=$(mktemp "$output_dir/.dune-status.XXXXXX")
  offset=$((page * limit))
  url="https://api.dune.com/api/v1/query/${query_id}/results/csv?limit=${limit}&offset=${offset}"
  if ! curl --fail --silent --show-error --retry 5 \
    --header "@$header_file" \
    --write-out '%{http_code}' \
    --output "$temp_file" \
    "$url" >"$status_temp"; then
    die "Dune page request failed"
  fi
  http_status=$(cat "$status_temp")
  rm -f -- "$status_temp"
  status_temp=
  case $http_status in
    2[0-9][0-9]) ;;
    *) die "Dune page request returned non-2xx status" ;;
  esac
  attempted=$((attempted + 1))

  csv_status=0
  python3 - "$temp_file" <<'PY' || csv_status=$?
import csv
import sys


def blank(row):
    return not row or all(not field.strip() for field in row)


def validate_quote_structure(path):
    field_start = "field_start"
    unquoted = "unquoted"
    quoted = "quoted"
    after_quote = "after_quote"
    after_quoted_cr = "after_quoted_cr"
    state = field_start
    with open(path, encoding="utf-8", newline="") as source:
        while chunk := source.read(1024 * 1024):
            for character in chunk:
                if state == quoted:
                    if character == '"':
                        state = after_quote
                elif state == after_quote:
                    if character == '"':
                        state = quoted
                    elif character == "," or character == "\n":
                        state = field_start
                    elif character == "\r":
                        state = after_quoted_cr
                    else:
                        raise ValueError("invalid CSV quote structure")
                elif state == after_quoted_cr:
                    if character != "\n":
                        raise ValueError("invalid CSV quote structure")
                    state = field_start
                elif state == field_start:
                    if character == '"':
                        state = quoted
                    elif character == "," or character == "\n":
                        state = field_start
                    else:
                        state = unquoted
                else:
                    if character == '"':
                        raise ValueError("invalid CSV quote structure")
                    if character == "," or character == "\n":
                        state = field_start
    if state == quoted or state == after_quoted_cr:
        raise ValueError("invalid CSV quote structure at end of file")


try:
    validate_quote_structure(sys.argv[1])
    with open(sys.argv[1], encoding="utf-8-sig", newline="") as response:
        reader = csv.reader(response, strict=True)
        header = None
        found_data = False
        for row in reader:
            if header is None:
                if blank(row):
                    continue
                header = row
                continue
            if blank(row):
                continue
            if len(row) != len(header):
                raise ValueError("CSV row width differs from header")
            found_data = True
except (OSError, UnicodeError, csv.Error, ValueError):
    raise SystemExit(2)

raise SystemExit(0 if found_data else 3)
PY
  case $csv_status in
    0) has_data=1 ;;
    3) has_data=0 ;;
    *) die "Dune page response is malformed CSV" ;;
  esac

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
    if ! ln -- "$temp_file" "$page_path"; then
      die "page appeared while download was in progress: $page_name"
    fi
    rm -f -- "$temp_file"
    temp_file=
  fi

  page=$((page + 1))
done

inventory_path=${output_dir}.sha256
inventory_temp=$(mktemp "${inventory_path}.tmp.XXXXXX")
LC_ALL=C
export LC_ALL
for page_path in "$output_dir"/"${query_id}_"[0-9][0-9][0-9][0-9].csv; do
  [ -f "$page_path" ] && [ ! -L "$page_path" ] || continue
  page_name=${page_path##*/}
  digest=$(sha256sum "$page_path" | awk '{print $1}')
  printf '%s  %s\n' "$digest" "$page_name" >>"$inventory_temp"
done
mv -- "$inventory_temp" "$inventory_path"
inventory_temp=
