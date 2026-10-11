package app

// Services is the complete, presentation-neutral boundary shared by TUI and web.
type Services interface {
	Explorer
	Ingestion
	Projections
	Searcher
	Assessor
}
