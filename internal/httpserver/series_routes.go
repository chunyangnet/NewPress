package httpserver

func (s *Server) registerSeriesRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /series/{$}", s.publicSeriesIndex)
	mux.HandleFunc("GET /series/{slug}/{$}", s.publicSeriesDetail)
	mux.HandleFunc("GET /admin/series/{$}", s.adminSeries)
	mux.HandleFunc("GET /api/v1/series", s.publicSeriesAPI)
	mux.HandleFunc("GET /api/v1/series/{slug}", s.publicSeriesAPI)
	mux.HandleFunc("GET /api/v1/admin/series", s.adminSeriesListAPI)
	mux.HandleFunc("POST /api/v1/admin/series", s.adminSeriesSaveAPI)
	mux.HandleFunc("GET /api/v1/admin/series/{id}", s.adminSeriesItemAPI)
	mux.HandleFunc("PUT /api/v1/admin/series/{id}", s.adminSeriesSaveAPI)
	mux.HandleFunc("DELETE /api/v1/admin/series/{id}", s.adminSeriesDeleteAPI)
	mux.HandleFunc("GET /api/v1/admin/series/sources", s.adminSeriesSourcesAPI)
	for _, method := range []string{"PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/series", s.adminAPIMethodNotAllowed("GET, POST"))
	}
	for _, method := range []string{"POST", "PATCH", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/series/{id}", s.adminAPIMethodNotAllowed("GET, PUT, DELETE"))
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/series/sources", s.adminAPIMethodNotAllowed("GET"))
	}
}
