# De-identified v1/v5 compatibility fixtures

These six files are **path-deidentified compatibility copies**, not original
experiment artifacts or a new model-quality measurement. They preserve the
`detective-reason-assessment/v1` and `detective-reason-adviser/v5` formats,
including the absence of thinking metadata. Their source versions came from
the former Detective implementation; the originals remain privately archived
outside this repository. No original machine paths or path mapping are kept
here.

The checkpoint and receipt source-path labels now both use the inert synthetic
path `/synthetic/ahe-core/docs/STATUS.md`. Tests do not open that path. The
existing Go integrity functions recomputed the checkpoint digest, receipt's
checkpoint reference, review-bundle digest, decision digest and assessment
digest from the changed bytes. **The hashes below identify these de-identified
copies only; they must not be cited as hashes of the original measured runs.**

Source text, candidate statements, scripted decisions/reasons, model metadata,
advisory text/verdicts, exact concern anchors, format versions and MCP
review/display/request identities are unchanged. The advisory content is
retained for compatibility testing, not semantic ground truth or human
approval. Regeneration calls no model, source, MCP tool or database.

`TestReasonThinkingLoadsDeidentifiedV1V5AssessmentsWithoutUpgrade` validates and
loads all six private test copies without upgrading their bytes or inferring
thinking metadata. `TestDeidentifiedCompatibilityFixtures` additionally checks
the deterministic transformation, its integrity chain and unchanged semantic
inputs. Ordinary tests never rewrite these fixtures.

The test-only generator can be explicitly invoked from the repository root:

```sh
go test -mod=readonly -p 2 -count=1 \
  -run '^TestDeidentifiedCompatibilityFixtures$' \
  ./apps/detective/internal/pending -args -update-deidentified-fixtures
```

It validates every input before transforming it and only writes these six
named files plus `../source_review.json`. After an intentional fixture change,
review and update the exact hashes below and the source-review regression
constants; do not disable the integrity assertions.

These six JSON files total 119,965 bytes. No request logs, model inventory,
original experiment directory, DB files, credentials or launchers are included.

| File | SHA-256 of de-identified copy |
| --- | --- |
| `audit-bounded.assessment.json` | `b0c679fcd14bff7a3926083c54cf4ed1497e03f6f6be621f2e862e16b4773b74` |
| `bounded-support.assessment.json` | `255f1347b07697ad16828292e4040a53eea0a84dc154b0e4b6cf3ea0193bf083` |
| `citation-only.assessment.json` | `ea2231255f99abc266d18f4321125b316aeaee564178d240ae5842f33f4395ce` |
| `pending-scope.assessment.json` | `0cd56f0b6e43261f38ab29df2a5b1b435955a098d80b364de9396fa6f5c08518` |
| `praise-paraphrase.assessment.json` | `6f220c4bb7ffa8a028d69e24f3d9c389d64f82488899222d998bcc5a5bae812c` |
| `reject-overclaim.assessment.json` | `64b93f29b9816983840bbd522357e9bb5d861af8552832ec731b32fcc0c1a527` |
