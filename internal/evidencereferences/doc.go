// Package evidencereferences validates exact source-to-source reference reviews.
// The supported anchor profiles are deterministic compatibility contracts, not
// arbitrary URL or natural-language resolution. Native writers reconstruct
// qualified source authority and atomically persist the reviewed edge and receipt;
// pure resolution or receipt construction alone does not authorize a write.
package evidencereferences
