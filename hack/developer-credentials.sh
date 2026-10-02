#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
credentials_file=${1:-"$repo_root/docs/developer/environment/docker-compose-data/credentials.env"}
config_file=${2:-"$repo_root/docs/developer/environment/trickster-config/trickster.generated.yaml"}
umask 077

if [ ! -f "$credentials_file" ]; then
    admin_password=$(openssl rand -hex 32)
    reader_password=$(openssl rand -hex 32)
    authorization=$(printf 'admin:%s' "$admin_password" | openssl base64 -A)
    credential_tmp=$(mktemp "$credentials_file.XXXXXX")
    trap 'rm -f "$credential_tmp"' EXIT HUP INT TERM
    {
        printf 'QDB_PG_PASSWORD=%s\n' "$admin_password"
        printf 'QDB_PG_READONLY_PASSWORD=%s\n' "$reader_password"
        printf 'QDB_HTTP_PASSWORD=%s\n' "$admin_password"
        printf 'QDB_HTTP_AUTHORIZATION=%s\n' "$authorization"
    } > "$credential_tmp"
    # Publish without overwriting credentials another concurrent startup may have created.
    ln "$credential_tmp" "$credentials_file" 2>/dev/null || test -f "$credentials_file"
    rm -f "$credential_tmp"
    trap - EXIT HUP INT TERM
fi

set -a
. "$credentials_file"
set +a
: "${QDB_PG_PASSWORD:?Missing QuestDB admin password}"
: "${QDB_PG_READONLY_PASSWORD:?Missing QuestDB reader password}"
: "${QDB_HTTP_PASSWORD:?Missing QuestDB HTTP password}"
: "${QDB_HTTP_AUTHORIZATION:?Missing QuestDB HTTP authorization}"

config_tmp=$(mktemp "$config_file.XXXXXX")
trap 'rm -f "$config_tmp"' EXIT HUP INT TERM
awk '
    { gsub(/\$\{QDB_PG_READONLY_PASSWORD\}/, ENVIRON["QDB_PG_READONLY_PASSWORD"])
      gsub(/\$\{QDB_HTTP_PASSWORD\}/, ENVIRON["QDB_HTTP_PASSWORD"])
      gsub(/\$\{QDB_HTTP_AUTHORIZATION\}/, ENVIRON["QDB_HTTP_AUTHORIZATION"])
      print }
' "$repo_root/docs/developer/environment/trickster-config/trickster.yaml" > "$config_tmp"
mv "$config_tmp" "$config_file"
trap - EXIT HUP INT TERM
