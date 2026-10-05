// Package runtimeconfig validates operator-owned runtime configuration.
package runtimeconfig

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

// PostgresURL accepts one explicit target and disables implicit credential files.
// Callers must reject ambient PG environment variables before invoking pgx.
func PostgresURL(value, database string) (string, error) {
	reject := errors.New("DATABASE_DSN must be an explicit single-target PostgreSQL URL without external credential or service files")
	if len(value) == 0 || len(value) > 16*1024 || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", reject
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" ||
		u.Fragment != "" || u.User == nil || u.User.Username() == "" || u.Path != "/"+database {
		return "", reject
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", reject
	}
	for key, values := range query {
		if len(values) != 1 || (key != "host" && key != "port" && key != "sslmode" && key != "connect_timeout") {
			return "", reject
		}
	}
	host := u.Hostname()
	if query.Has("host") {
		if u.Host != "" {
			return "", reject
		}
		host = query.Get("host")
	}
	if host == "" || strings.ContainsAny(host, ",\r\n\x00") || (u.Port() != "" && query.Has("port")) {
		return "", reject
	}
	switch query.Get("sslmode") {
	case "disable", "require", "verify-full":
	default:
		return "", reject
	}
	if strings.HasPrefix(host, "/") && query.Get("sslmode") != "disable" {
		// pgx never uses TLS on a Unix socket. Refuse a contradictory TLS
		// request instead of reporting an encrypted connection that is not.
		return "", reject
	}
	query.Set("passfile", "/dev/null")
	query.Set("sslcert", "")
	query.Set("sslkey", "")
	query.Set("sslrootcert", "")
	if query.Get("sslmode") == "verify-full" {
		query.Set("sslrootcert", "system")
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}
