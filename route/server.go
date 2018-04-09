package route

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"regexp"
	noesctmpl "text/template"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/elazarl/go-bindata-assetfs"
	"github.com/gorilla/websocket"

	"github.com/wrfly/container-web-tty/container"
	"github.com/wrfly/container-web-tty/gotty/webtty"
)

// Server provides a webtty HTTP endpoint.
type Server struct {
	factory      Factory
	options      *Options
	containerCli container.Cli

	upgrader      *websocket.Upgrader
	indexTemplate *template.Template
	listTemplate  *template.Template
	titleTemplate *noesctmpl.Template
}

// New creates a new instance of Server.
// Server will use the New() of the factory provided to handle each request.
func New(factory Factory, options *Options, containerCli container.Cli) (*Server, error) {
	indexData, err := Asset("static/index.html")
	if err != nil {
		panic("index not found") // must be in bindata
	}
	indexTemplate, err := template.New("index").Parse(string(indexData))
	if err != nil {
		panic("index template parse failed") // must be valid
	}

	listIndexData, err := Asset("static/list.html")
	if err != nil {
		panic("list index not found") // must be in bindata
	}
	listTemplate, err := template.New("list").Parse(string(listIndexData))
	if err != nil {
		panic("list template parse failed") // must be valid
	}

	titleTemplate, err := noesctmpl.New("title").Parse(options.TitleFormat)
	if err != nil {
		return nil, fmt.Errorf("failed to parse window title format `%s`", options.TitleFormat)
	}

	var originChekcer func(r *http.Request) bool
	if options.WSOrigin != "" {
		matcher, err := regexp.Compile(options.WSOrigin)
		if err != nil {
			return nil, fmt.Errorf("failed to compile regular expression of Websocket Origin: %s", options.WSOrigin)
		}
		originChekcer = func(r *http.Request) bool {
			return matcher.MatchString(r.Header.Get("Origin"))
		}
	}

	return &Server{
		factory:      factory,
		options:      options,
		containerCli: containerCli,

		upgrader: &websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			Subprotocols:    webtty.Protocols,
			CheckOrigin:     originChekcer,
		},
		indexTemplate: indexTemplate,
		titleTemplate: titleTemplate,
		listTemplate:  listTemplate,
	}, nil
}

// Run starts the main process of the Server.
// The cancelation of ctx will shutdown the server immediately with aborting
// existing connections. Use WithGracefullContext() to support gracefull shutdown.
func (server *Server) Run(ctx context.Context, options ...RunOption) error {
	cctx, cancel := context.WithCancel(ctx)
	opts := &RunOptions{gracefullCtx: context.Background()}
	for _, opt := range options {
		opt(opts)
	}

	counter := newCounter(time.Duration(server.options.Timeout) * time.Second)

	router := gin.Default()

	h := http.FileServer(
		&assetfs.AssetFS{Asset: Asset, AssetDir: AssetDir, Prefix: "static"},
	)
	fh := gin.WrapH(http.StripPrefix("/", h))

	// Routes
	router.GET("/", server.handleListContainers)
	router.GET("/js/:x", fh)
	router.GET("/css/:x", fh)
	router.GET("/favicon.png", fh)

	router.GET("/auth_token.js", server.handleAuthToken)
	router.GET("/config.js", server.handleConfig)

	router.GET("/c/:id", func(c *gin.Context) {
		c.Redirect(301, c.Request.URL.String()+"/")
	})
	router.GET("/c/:id/", server.handleIndex)
	router.GET("/c/:id/"+"ws", func(c *gin.Context) {
		id := c.Param("id")
		server.generateHandleWS(ctx, cancel, counter, id).ServeHTTP(c.Writer, c.Request)
	})

	hostPort := net.JoinHostPort(server.options.Address, server.options.Port)
	srv := &http.Server{
		Addr:    hostPort,
		Handler: router,
	}

	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.ListenAndServe()
	}()

	go func() {
		select {
		case <-opts.gracefullCtx.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				log.Fatal("Server Shutdown:", err)
			}
		case <-cctx.Done():
		}
	}()

	var err error
	select {
	case err = <-srvErr:
		if err == http.ErrServerClosed { // by gracefull ctx
			err = nil
		} else {
			cancel()
		}
	case <-cctx.Done():
		srv.Close()
		err = cctx.Err()
	}

	conn := counter.count()
	if conn > 0 {
		log.Printf("Waiting for %d connections to be closed", conn)
	}
	counter.wait()

	return err
}
