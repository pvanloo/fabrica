package web

import (
	"fmt"
	"github.com/gorilla/mux"
	"github.com/ogra1/fabrica/config"
	"github.com/ogra1/fabrica/service/key"
	"github.com/ogra1/fabrica/service/lxd"
	"github.com/ogra1/fabrica/service/repo"
	"github.com/ogra1/fabrica/service/system"
	"net/http"
)

// Web implements the web service
type Web struct {
	Settings  *config.Settings
	BuildSrv  repo.BuildSrv
	LXDSrv    lxd.Service
	SystemSrv system.Srv
	KeySrv    key.Srv
}

// NewWebService starts a new web service
func NewWebService(settings *config.Settings, bldSrv repo.BuildSrv, lxdSrv lxd.Service, systemSrv system.Srv, keySrv key.Srv) *Web {
	return &Web{
		Settings:  settings,
		BuildSrv:  bldSrv,
		LXDSrv:    lxdSrv,
		SystemSrv: systemSrv,
		KeySrv:    keySrv,
	}
}

// Start the web service
func (srv Web) Start() error {
	listenOn := fmt.Sprintf("%s:%s", "0.0.0.0", srv.Settings.Port)
	fmt.Printf("Starting service on port %s\n", listenOn)
	return http.ListenAndServe(listenOn, srv.Router())
}

// Router returns the application router
func (srv Web) Router() *mux.Router {
	// Start the web service router
	router := mux.NewRouter()

	router.Handle("/v1/repos", Middleware(http.HandlerFunc(srv.RepoList))).Methods("GET")
	router.Handle("/v1/repos", Middleware(http.HandlerFunc(srv.RepoCreate))).Methods("POST")
	router.Handle("/v1/repos/delete", Middleware(http.HandlerFunc(srv.RepoDelete))).Methods("POST")

	router.Handle("/v1/check/images", Middleware(http.HandlerFunc(srv.ImageAliases))).Methods("GET")
	router.Handle("/v1/check/connections", Middleware(http.HandlerFunc(srv.CheckConnections))).Methods("GET")

	router.Handle("/v1/build", Middleware(http.HandlerFunc(srv.Build))).Methods("POST")
	router.Handle("/v1/builds", Middleware(http.HandlerFunc(srv.BuildList))).Methods("GET")
	router.Handle("/v1/builds/{id}/download", Middleware(http.HandlerFunc(srv.BuildDownload))).Methods("GET")
	router.Handle("/v1/builds/{id}", Middleware(http.HandlerFunc(srv.BuildLog))).Methods("GET")
	router.Handle("/v1/builds/{id}", Middleware(http.HandlerFunc(srv.BuildDelete))).Methods("DELETE")

	router.Handle("/v1/system", Middleware(http.HandlerFunc(srv.SystemResources))).Methods("GET")
	router.Handle("/v1/system/environment", Middleware(http.HandlerFunc(srv.Environment))).Methods("GET")

	router.Handle("/v1/keys", Middleware(http.HandlerFunc(srv.KeyList))).Methods("GET")
	router.Handle("/v1/keys", Middleware(http.HandlerFunc(srv.KeyCreate))).Methods("POST")
	router.Handle("/v1/keys/{id}", Middleware(http.HandlerFunc(srv.KeyDelete))).Methods("DELETE")

	// Serve the static path
	fs := http.StripPrefix("/static/", http.FileServer(http.Dir(docRoot)))
	router.PathPrefix("/static/").Handler(fs)

	// Default path is the index page
	router.Handle("/", Middleware(http.HandlerFunc(srv.Index))).Methods("GET")
	router.Handle("/builds/{id}", Middleware(http.HandlerFunc(srv.Index))).Methods("GET")
	router.Handle("/builds/{id}/download", Middleware(http.HandlerFunc(srv.Index))).Methods("GET")
	router.Handle("/settings", Middleware(http.HandlerFunc(srv.Index))).Methods("GET")

	return router
}
