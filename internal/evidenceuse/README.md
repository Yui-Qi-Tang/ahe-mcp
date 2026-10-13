# Evidence use policies

`current-action/v1` and `grounded-current-action/v1` preserve withdrawal, source
assessments, supersession, temporal state, direct conflicts and full AND ancestry
as distinct dimensions. They do not establish truth, entailment or permission.

The PostgreSQL adapter and native writer/Query/consumer integration are described
in [GOVERNANCE.md](../evidenceingestion/GOVERNANCE.md). `CheckPlan` is a pure
assessment; operational consumers use `CheckCurrentEvidencePlan` to reread DB
state with an explicitly selected versioned policy, and native current derivation
binds `use_policy` in its review and repeats it under the transaction guard.
The grounded policy requires passing mandatory source checks; missing or
inconclusive findings are not a pass. Current-action APIs reject unknown or
omitted purposes instead of silently choosing a weaker policy.

Verification reuse is separate: every potential hit requires a live recheck of
the complete material set. It cannot clear a governance exclusion or replace the
final current-use check. All tests use synthetic fixtures.
