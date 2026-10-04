//go:build race

package session

// poisonReusedData makes the reader overwrite each reused DATA payload after
// it is handled, so a retained slice corrupts data and fails tests (race
// builds only; CI runs the suite with -race).
const poisonReusedData = true
