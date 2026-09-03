package server

// edition_community.go names this build. See edition_enterprise.go for why the
// edition is expressed as constants rather than read from a build tag at each
// call site.

// Edition is reported by /health and /api/v1/auth/methods.
const Edition = "community"

const (
	// This build ships neither the single sign-on provider nor the team
	// boundary. Both are reported as unavailable rather than simply absent, so
	// that a person looking for them learns where they went.
	editionHasSSO         = false
	editionHasTeamScoping = false
)
