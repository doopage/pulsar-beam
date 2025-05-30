package route

import (
	"net/http"
	"net/http/pprof"

	"github.com/gorilla/mux"

	"github.com/kafkaesque-io/pulsar-beam/src/middleware"
	"github.com/kafkaesque-io/pulsar-beam/src/util"
	log "github.com/sirupsen/logrus"
)

// NewRouter - create new router for HTTP routing
func NewRouter(mode *string) *mux.Router {

	router := mux.NewRouter().StrictSlash(true)
	for _, route := range GetEffectiveRoutes(mode) {
		var handler http.Handler

		handler = route.HandlerFunc
		handler = Logger(handler, route.Name)

		router.
			Methods(route.Method).
			Path(route.Pattern).
			Name(route.Name).
			Handler(route.AuthFunc(handler))
	}
	
	router.Handle("/debug/pprof", http.HandlerFunc(pprof.Index))
	router.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	router.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	router.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	router.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	router.Handle("/debug/pprof/block", pprof.Handler("block"))
	router.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	router.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	
	// TODO rate limit can be added per route basis
	router.Use(middleware.LimitRate)

	log.Infof("router added")
	return router
}

// GetEffectiveRoutes gets effective routes
func GetEffectiveRoutes(mode *string) Routes {
	return append(PrometheusRoute, getRoutes(mode)...)
}

func getRoutes(mode *string) Routes {
	switch *mode {
	case util.Hybrid:
		return append(ReceiverRoutes, RestRoutes...)
	case util.Receiver:
		return ReceiverRoutes
	case util.HTTPOnly:
		return append(ReceiverRoutes, append(RestRoutes, TokenServerRoutes...)...)
	case util.TokenServer:
		return TokenServerRoutes
	case util.HTTPWithNoRest:
		return append(ReceiverRoutes, TokenServerRoutes...)
	default:
		return RestRoutes
	}
}
