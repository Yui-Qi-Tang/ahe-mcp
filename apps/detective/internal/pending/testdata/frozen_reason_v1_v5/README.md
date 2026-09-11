# Frozen v1/v5 compatibility fixtures

These six files are exact copies from Detective commit
`a828b49041bbd341f887212da75d898c8718edb4`, under
`docs/artifacts/reason_target_20260908_run/`. They are required by the ordinary
`TestReasonThinkingLoadsFrozenV1V5AssessmentsWithoutUpgrade` regression test,
not optional live-model experiments.

The original experiment used the AHE project's own status-document excerpt and
six scripted operator reasons. Its frozen plan records `mcp_calls: 0`,
`database_used: false`, `writer_calls: 0`, and `human_approval: false`. The stored
model assessments are historical output, not semantic ground truth or human
approval. Inspection found no company content, credentials, or private
conversation text in these selected files.

The original source path remains as inert, hash-bound provenance inside the
checkpoint. Tests read the JSON copied here, never that historical absolute
path. Do not rewrite provenance, schema, prompt version, decisions, model text,
or digests to make the fixture match a newer runtime.

Only these six JSON files (120,757 bytes total) were imported, not the original
experiment directory, request logs, model inventory, DB files, or launchers.

| File | SHA-256 |
| --- | --- |
| `audit-bounded.assessment.json` | `f57ec34f3d87627a17086355a4ae7ba432da241184a614bae10757d443adee2b` |
| `bounded-support.assessment.json` | `3229a0c00afa1a39367a068ca502d3e3bcd00000de5b3f43ed6b9631fd792fb9` |
| `citation-only.assessment.json` | `0f196e6851c02587d5c320f22deabe27c85b11b58807629dc374fe59542c90c9` |
| `pending-scope.assessment.json` | `3084ed885df58838541c0ef127a0131a0bc275104ab9c9d7a9605f193ac46cd0` |
| `praise-paraphrase.assessment.json` | `280247d746dea4a34b0699d21ccf2045c540c8499ba23f44fa14a421ee6d1d23` |
| `reject-overclaim.assessment.json` | `425390eeff151f6efcf548c274a22f059dd58c8e8c129cd8384e69fcacff4946` |
