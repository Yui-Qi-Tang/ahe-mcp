package mcplaunch

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// databaseURL is deliberately not a general libpq parser. V1 accepts one
// explicit target URL without services, external files, runtime options or
// fallback hosts. Parsing here neither reads default credentials nor mutates
// the process environment; the MCP runtime verifies the actual DB binding.
func databaseURL(body []byte, cfg config) (string, error) {
	if len(body) == 0 || len(body) > maxCredentialBytes || !utf8.Valid(body) {
		return "", errCredential
	}
	raw := strings.TrimSuffix(string(body), "\n")
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", errCredential
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" ||
		u.User == nil || u.User.Username() != cfg.SessionUser || u.Path != "/"+cfg.Database {
		return "", errCredential
	}
	if password, _ := u.User.Password(); strings.ContainsFunc(password, unicode.IsControl) {
		return "", errCredential
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errCredential
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" || strings.ContainsFunc(values[0], unicode.IsControl) {
			return "", errCredential
		}
		switch key {
		case "host", "port", "sslmode", "connect_timeout":
		default:
			return "", errCredential
		}
	}
	host := u.Hostname()
	port := u.Port()
	if query.Has("host") {
		if u.Host != "" {
			return "", errCredential
		}
		host = query.Get("host")
	}
	if host == "" || strings.ContainsAny(host, ", \\@") {
		return "", errCredential
	}
	if strings.HasPrefix(host, "/") {
		// pgx does not use TLS on Unix sockets. Require the explicit local
		// transport choice instead of accepting a TLS promise it cannot keep.
		if !absolutePath(host) || query.Get("sslmode") != "disable" {
			return "", errCredential
		}
	} else if net.ParseIP(host) == nil {
		for _, character := range host {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-') {
				return "", errCredential
			}
		}
	}
	if query.Has("port") {
		if port != "" {
			return "", errCredential
		}
		port = query.Get("port")
	}
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 || strconv.Itoa(value) != port {
			return "", errCredential
		}
	}
	if timeout := query.Get("connect_timeout"); timeout != "" {
		value, err := strconv.Atoi(timeout)
		if err != nil || value < 1 || value > 300 || strconv.Itoa(value) != timeout {
			return "", errCredential
		}
	}
	switch query.Get("sslmode") {
	case "disable", "require":
		query.Set("sslrootcert", "")
	case "verify-full":
		query.Set("sslrootcert", "system")
	default:
		return "", errCredential
	}
	// pgx always attempts its passfile read, even with an explicit password.
	// Empty TLS file settings suppress implicit ~/.postgresql files. No service
	// key is added: even an empty service value would trigger servicefile reads.
	query.Set("passfile", "/dev/null")
	query.Set("sslcert", "")
	query.Set("sslkey", "")
	u.RawQuery = query.Encode()
	return u.String(), nil
}
