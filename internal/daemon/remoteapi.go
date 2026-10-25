package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/remoteapi"
	"github.com/Hisha/Forgehand/internal/version"
)

// remoteAPITimeouts are the server level timeouts of the network listener.
// They bound how long a single slow client can occupy a connection.
const (
	remoteAPIReadHeaderTimeout = 10 * time.Second
	remoteAPIReadTimeout       = 30 * time.Second
	remoteAPIWriteTimeout      = 60 * time.Second
	remoteAPIIdleTimeout       = 120 * time.Second
	remoteAPIMaxHeaderBytes    = 16 << 10
)

// startRemoteAPI starts the optional network listener of the daemon.
//
// The listener is disabled unless FORGEHAND_API_ADDRESS is configured, and
// the configuration only accepts a loopback address. A failure to start the
// listener is fatal so that a requested but unavailable API never degrades
// into a partially running daemon.
//
// It returns a nil server when the network listener is disabled.
func startRemoteAPI(
	ctx context.Context,
	accounts *accountManager,
	operationOptions ...remoteapi.SessionOperations,
) (*http.Server, error) {
	apiConfig, enabled, err := config.API()
	if err != nil {
		return nil, fmt.Errorf("load remote API configuration: %w", err)
	}
	if !enabled {
		return nil, nil
	}

	service, err := accounts.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("prepare remote API authentication: %w", err)
	}

	var operations remoteapi.SessionOperations
	if len(operationOptions) > 0 {
		operations = operationOptions[0]
	}
	api, err := remoteapi.NewServer(remoteapi.Options{
		Service:       service,
		Version:       version.Version,
		BuildIdentity: version.BuildIdentity(),
		Sessions:      operations,
	})
	if err != nil {
		return nil, fmt.Errorf("create remote API server: %w", err)
	}

	listener, err := net.Listen("tcp", apiConfig.Address)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", apiConfig.Address, err)
	}

	server := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: remoteAPIReadHeaderTimeout,
		ReadTimeout:       remoteAPIReadTimeout,
		WriteTimeout:      remoteAPIWriteTimeout,
		IdleTimeout:       remoteAPIIdleTimeout,
		MaxHeaderBytes:    remoteAPIMaxHeaderBytes,
		// The default server log records request lines. Forgehand does not
		// log remote requests, so the logger is discarded to keep bearer
		// credentials and query strings out of any log.
		ErrorLog: log.New(io.Discard, "", 0),
	}

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "forgehand: remote API stopped: %v\n", serveErr)
		}
	}()

	fmt.Printf("Remote API listening on %s (loopback only; API version %s)\n", apiConfig.Address, remoteapi.APIVersion)

	return server, nil
}
