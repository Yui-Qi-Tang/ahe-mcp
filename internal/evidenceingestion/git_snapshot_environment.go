package evidenceingestion

// No caller GIT_DIR, config injection, credentials, helpers or replacement objects
// cross this boundary. Empty GIT_ALLOW_PROTOCOL rejects every transport, including
// file and external helpers; missing promisor objects must fail, never lazy-fetch.
// This controls trusted Git, not an OS sandbox against a substituted executable.
func offlineGitEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ALLOW_PROTOCOL=", "GIT_PROTOCOL_FROM_USER=0", "GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1",
	}
}
