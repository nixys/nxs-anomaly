package server

import "net/http"

// capabilities.go answers what this installation can do, and — when it cannot
// do something — which of the two reasons applies.
//
// Two reasons, not one, is the whole point of the endpoint. "Single sign-on is
// off" is useless to whoever reads it: an operator who has simply not set an
// issuer needs to go and set one, while an operator on an edition that ships no
// provider needs to know that no amount of configuration will help. Collapsing
// those into one state is what makes a product feel broken instead of bounded.
//
// The endpoint is authenticated. The sign-in screen, which runs before anyone
// has a session, gets the one bit it needs (the edition) from
// /api/v1/auth/methods instead — that endpoint's rule is that it says which
// doors exist and never whether one would open, and naming the edition does not
// break it, because the edition is already written on the image tag.
const (
	// capAvailable: the feature ships in this build and is configured.
	capAvailable = "available"
	// capNotInEdition: the build does not contain it. Configuration cannot fix
	// this; a different edition can.
	capNotInEdition = "unavailable_in_edition"
	// capNotConfigured: the build contains it and this deployment has not set
	// it up. The operator can fix this.
	capNotConfigured = "not_configured"
)

// capability is one feature and its state, with a sentence a person can act on.
type capability struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// capabilities reports the features whose availability depends on the edition
// or on configuration. Features that every build has and needs no setting are
// deliberately absent: a list that includes everything stops being read.
func (srv *Server) capabilities() []capability {
	return []capability{
		srv.ssoCapability(),
		srv.teamScopingCapability(),
		srv.analyticsCapability(),
	}
}

func (srv *Server) ssoCapability() capability {
	c := capability{Name: "sso"}
	switch {
	case !editionHasSSO:
		c.State = capNotInEdition
		c.Detail = "Single sign-on through an OIDC provider is not part of the community edition."
	case srv.oidc == nil:
		c.State = capNotConfigured
		c.Detail = "Set NXS_ANOMALY_OIDC_ISSUER, NXS_ANOMALY_OIDC_CLIENT_ID and NXS_ANOMALY_OIDC_REDIRECT_URL to enable it."
	default:
		c.State = capAvailable
		c.Detail = "Sign-in through " + oidcLabel(srv.oidc.cfg.Issuer) + " is enabled."
	}
	return c
}

func (srv *Server) teamScopingCapability() capability {
	c := capability{Name: "team_scoping"}
	switch {
	case !editionHasTeamScoping:
		c.State = capNotInEdition
		c.Detail = "Limiting what each team sees is not part of the community edition; every operator sees every integration and schedule."
	case !srv.cfg.TeamScoping:
		c.State = capNotConfigured
		c.Detail = "Set NXS_ANOMALY_TEAM_SCOPING=true to limit each person to the objects of their teams."
	default:
		c.State = capAvailable
		c.Detail = "Each person sees the objects of their teams, plus the unassigned ones."
	}
	return c
}

func (srv *Server) analyticsCapability() capability {
	c := capability{Name: "analytics_stream"}
	switch {
	case !srv.eng.AnalyticsSupported():
		c.State = capNotInEdition
		c.Detail = "The Kafka analytics stream is not part of the community edition."
	case !srv.eng.AnalyticsConfigured():
		c.State = capNotConfigured
		c.Detail = "Set KAFKA_BROKERS to publish lifecycle events for analytics."
	default:
		c.State = capAvailable
		c.Detail = "Lifecycle events are published to Kafka through the transactional outbox."
	}
	return c
}

// handleCapabilities serves the report.
func (srv *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"edition":      Edition,
		"capabilities": srv.capabilities(),
	})
}
