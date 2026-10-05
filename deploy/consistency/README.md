# Foreground worker supervision templates

These templates do not install or start a service. Adjust the absolute paths,
service account and credential environment for the selected deployment. Build
`ahe-consistency-worker` first. Supply its documented environment in a private
operator-owned file (0600) under trusted directories; never commit credentials.
For the launchd shell wrapper, the file is trusted shell assignments, not JSON.
Do not put untrusted source content in it. The systemd EnvironmentFile format is
plain assignments; keep values compatible with that manager's quoting rules.

- Linux: the unit uses `Restart=on-failure`, five-second delay and a restart
  limit. When the limit is reached, correct the cause and reset/restart it.
- macOS: the plist runs the shell wrapper with two fixed operator-selected paths;
  `exec` preserves signals and exit status. launchd restarts unsuccessful exits
  with a throttle. Use bootout to stop supervision before maintenance, not just
  killing the process. SIGTERM is a clean worker stop.

Database/engine errors cannot be repaired by repeatedly restarting. Read/pause or
correct the affected watch and verify the database policy. A recorded solver
failure needs a new configuration revision. Monitor process health and query
stored events/results separately: a running process is not proof of a successful
diagnosis. Filesystem retention and service-manager installation are operator
responsibilities. Shell and plist syntax and the unit's static directives are
checked; the service managers have not been installed or exercised by this
repository validation. These checks are not deployment qualification.

See the [product workflow](../../docs/CONSISTENCY.md) and [upgrade](../../INSTALL.md#upgrade).
