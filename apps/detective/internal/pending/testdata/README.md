# Pending compatibility test data

`source_review.json` is a **path-deidentified copy of a legacy-format review**,
not an original measured artifact, current pending-state observation or human
approval. Its source path is an inert synthetic label; tests reconstruct the
document from the JSON and never open that path.

The existing integrity rules recomputed its checkpoint, receipt checkpoint
reference and review-bundle digest after replacing the two machine-path labels.
The source text, candidate, MCP review/display and native request identity stay
unchanged. The original artifact remains privately archived outside Git; this
directory contains no original-path mapping.

| De-identified coordinate | Value |
| --- | --- |
| File bytes | `10306` |
| File SHA-256 | `46711eaaf4be85f62b12d41e553ed9ea9e001a9549137f30f6f90a149a4e9ef6` |
| Review-bundle digest | `sha256:4f6042af4c1896038c3f99db3466aec76e882206043878be88101bcddca84858` |
| Checkpoint digest | `sha256:c2df91ee235e913776cf4c2ce198f54c1510b9e31441f4b5963b5ae59b3cd974` |

`TestStatementDeidentifiedLegacyNativeReviewUnchanged` pins these new values
and the preserved native request ID, then checks that loading does not modify
the saved bytes. These hashes must not be described as original-run digests.

See the [de-identified assessment fixtures](frozen_reason_v1_v5/README.md) for
the other six files, their precise hashes and the explicit test-only generator.
No generator or compatibility test calls a model, MCP tool, source or DB.
