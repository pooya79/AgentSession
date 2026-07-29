package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
)

// search renders one bounded result page while keeping validation errors safe
// for presentation.
func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	values, ok := strictQuery(w, r, "q", "cursor")
	if !ok {
		return
	}
	query, ok := optionalSingleAllowEmpty(w, values, "q")
	if !ok {
		return
	}
	cursor, ok := optionalSingle(w, values, "cursor")
	if !ok {
		return
	}
	view := searchView{Query: query}
	page, err := h.services.Search(r.Context(), app.SearchRequest{
		Query: query, Cursor: cursor, Limit: defaultPageLimit,
	})
	if err != nil {
		var validation *app.SearchValidationError
		if !errors.As(err, &validation) {
			writeServiceError(w, err)
			return
		}
		view.Err = validation
	} else {
		view.Page = page
	}
	render(w, r, http.StatusOK, searchPage(view))
}

func availabilityMessage(value app.SearchAvailability) string {
	switch value.State {
	case app.EvidenceUnavailable:
		return "Search is unavailable: no imported session has a current ready index."
	case app.EvidencePartial:
		return "Search is partial: " + formatCount(value.Usable, "session") + " searchable; " +
			formatCount(value.Pending+value.Running+value.Failed, "session") + " not currently searchable."
	default:
		if value.Sessions == 0 {
			return "Search is ready; import sessions to create searchable evidence."
		}
		return "Search is complete across " + formatCount(value.Usable, "session") + "."
	}
}

func compactSnippet(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
