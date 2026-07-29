package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/pooya79/AgentSession/internal/app"
)

func (h *handler) search(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query().Get("q")
	cursor := request.URL.Query().Get("cursor")
	view := searchView{Query: query}
	page, err := h.services.Search(request.Context(), app.SearchRequest{
		Query: query, Cursor: cursor, Limit: defaultPageLimit,
	})
	if err != nil {
		var validation *app.SearchValidationError
		if !errors.As(err, &validation) {
			http.Error(response, "Search is temporarily unavailable.", http.StatusInternalServerError)
			return
		}
		view.Err = validation
	} else {
		view.Page = page
	}
	if err := searchPage(view).Render(request.Context(), response); err != nil {
		http.Error(response, "Search is temporarily unavailable.", http.StatusInternalServerError)
	}
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
