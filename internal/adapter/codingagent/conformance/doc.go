// Package conformance is the §13.3 coding-agent conformance suite ("forge plugin
// test"). It validates that an adapter honours the protocol: handshake & version
// compatibility, event ordering, malformed output resilience, cancellation,
// timeout handling, quota failure classification, resume, and process-crash
// classification.
//
// The suite is scenario-driven: it requests an adapter configured for each
// [fake.Scenario] via an [AdapterFactory]. The fake coding agent honours every
// scenario, so the suite is fully exercised against it (M2-7, AC-6); a
// real-world plugin typically honours only the success path and the metadata
// checks. Adding a new coding agent that passes this suite requires no changes
// to the scheduler, schema, dashboard or routing core (§13.3).
package conformance
