#!/bin/sh
set -eu

: "${AHE_DETECTIVE_BINARY:?AHE_DETECTIVE_BINARY is required}"
: "${AHE_DETECTIVE_CONFIG:?AHE_DETECTIVE_CONFIG is required}"
: "${AHE_DATABASE_DNS_FILE:?AHE_DATABASE_DNS_FILE is required}"

case "$AHE_DETECTIVE_BINARY" in
	/*) ;;
	*) echo "AHE_DETECTIVE_BINARY must be absolute" >&2; exit 2 ;;
esac
case "$AHE_DETECTIVE_CONFIG" in
	/*) ;;
	*) echo "AHE_DETECTIVE_CONFIG must be absolute" >&2; exit 2 ;;
esac
case "$AHE_DATABASE_DNS_FILE" in
	/*) ;;
	*) echo "AHE_DATABASE_DNS_FILE must be absolute" >&2; exit 2 ;;
esac

if [ ! -x "$AHE_DETECTIVE_BINARY" ]; then
	echo "AHE_DETECTIVE_BINARY is not executable" >&2
	exit 2
fi
if [ ! -r "$AHE_DETECTIVE_CONFIG" ]; then
	echo "AHE_DETECTIVE_CONFIG is not readable" >&2
	exit 2
fi
if [ ! -f "$AHE_DATABASE_DNS_FILE" ]; then
	echo "AHE_DATABASE_DNS_FILE is not a regular file" >&2
	exit 2
fi
if [ -L "$AHE_DATABASE_DNS_FILE" ]; then
	echo "AHE_DATABASE_DNS_FILE must not be a symbolic link" >&2
	exit 2
fi

credential_mode=$(/usr/bin/stat -f '%Lp' "$AHE_DATABASE_DNS_FILE")
case "$credential_mode" in
	400|600) ;;
	*)
		echo "AHE_DATABASE_DNS_FILE mode must be 0400 or 0600" >&2
		exit 2
		;;
esac

DATABASE_DNS=$(/bin/cat "$AHE_DATABASE_DNS_FILE")
if [ -z "$DATABASE_DNS" ]; then
	echo "AHE_DATABASE_DNS_FILE is empty" >&2
	exit 2
fi
case "$DATABASE_DNS" in
	*'
'*)
		echo "AHE_DATABASE_DNS_FILE must contain exactly one line" >&2
		exit 2
		;;
esac
export DATABASE_DNS

exec "$AHE_DETECTIVE_BINARY" --config "$AHE_DETECTIVE_CONFIG"
