package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// SupportedLocales are the UI languages this build ships translations for.
//
// The list lives in the backend, not only in the frontend, because the value is
// persisted on a user record: without a gate here, any API client could write
// `fr-CA` into a profile and the UI would silently fall back forever, with
// nothing to point at as the cause.
var SupportedLocales = []string{"ru-RU", "en-US"}

// SanitizeLocale accepts a supported locale tag or the empty string.
//
// Empty is a real, distinct value: it means nobody has chosen a language yet,
// and the UI should follow the browser. Defaulting to a language here would
// turn "not asked" into "answered", and would override the browser preference
// of every user created before this field existed.
func SanitizeLocale(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", raw))
	if s == "" {
		return "", nil
	}
	for _, supported := range SupportedLocales {
		// Case-insensitive because language tags are case-insensitive by
		// definition (BCP 47), and `ru-ru` is what a hand-written client sends.
		if strings.EqualFold(s, supported) {
			return supported, nil
		}
	}
	return "", errValidation(fmt.Sprintf("locale must be one of %s (got %q)", strings.Join(SupportedLocales, ", "), s))
}

// UpdateUserPreferences writes the presentation settings a person owns about
// themselves: the UI language, and the timezone their timestamps are rendered
// in.
//
// It is deliberately separate from UpdateUser. Editing a user is an
// administrative act — it can change a role, and therefore what somebody may
// do — while choosing your own language is not, and a viewer must be able to do
// it. Keeping them apart means the self-service endpoint cannot be used to
// reach any field that governs permissions.
func (e *Engine) UpdateUserPreferences(ctx context.Context, userID string, payload map[string]any) (map[string]any, error) {
	locale, hasLocale := payload["locale"]
	timezone, hasTimezone := payload["timezone"]
	if !hasLocale && !hasTimezone {
		return nil, errValidation("nothing to update: expected locale and/or timezone")
	}

	var cleanLocale string
	if hasLocale {
		var err error
		if cleanLocale, err = SanitizeLocale(locale); err != nil {
			return nil, err
		}
	}
	var cleanTimezone string
	if hasTimezone {
		name := strings.TrimSpace(fmt.Sprintf("%v", timezone))
		if name != "" {
			if _, err := time.LoadLocation(name); err != nil {
				return nil, errValidation(fmt.Sprintf("unknown timezone: %s", name))
			}
		}
		cleanTimezone = name
	}

	ts := utils.ToISO(utils.UTCNow())
	result, err := e.store.UpdateCollectionsFiltered(ctx, loadItems("users", userID), []string{"users"},
		func(state *store.State) (any, error) {
			user := state.Users[userID]
			if user == nil {
				return nil, errNotFound(fmt.Sprintf("user %s not found", userID))
			}
			changed := map[string]any{}
			if hasLocale {
				user["locale"] = cleanLocale
				changed["locale"] = cleanLocale
			}
			// An empty timezone falls back to UTC rather than being stored as a
			// blank: every other read path treats the field as always present.
			if hasTimezone {
				user["timezone"] = strDefault(cleanTimezone, "UTC")
				changed["timezone"] = user["timezone"]
			}
			user["updated_at"] = ts
			e.auditIn(state, ctx, AuditUpdate, "user", userID, changed)
			return user, nil
		}, advisoryLock["update_user"])
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}
