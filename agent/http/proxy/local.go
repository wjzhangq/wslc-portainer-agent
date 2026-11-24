package proxy

import (
	"io"
	"net/http"

	"github.com/portainer/agent/wslc"
	httperror "github.com/portainer/portainer/pkg/libhttp/error"

	"github.com/rs/zerolog/log"
)

// LocalProxy is a service used to proxy requests to a Unix socket (Linux) or named pipe (Windows).
// The proxy operation implementation is defined in the ServeHTTP function.
// When wslc is set, requests are served by the wslc backend instead of a Docker socket.
type LocalProxy struct {
	transport *http.Transport
	wslc      http.Handler
}

func newWslcProxy() (*LocalProxy, error) {
	handler, err := wslc.NewHandler()
	if err != nil {
		return nil, err
	}
	return &LocalProxy{wslc: handler}, nil
}

func (proxy *LocalProxy) ServeHTTP(rw http.ResponseWriter, request *http.Request) {
	if proxy.wslc != nil {
		proxy.wslc.ServeHTTP(rw, request)
		return
	}
	if proxy.transport == nil {
		http.Error(rw, "docker proxy is not configured", http.StatusInternalServerError)
		return
	}

	request.URL.Scheme = "http"
	request.URL.Host = "unixsocket"

	res, err := proxy.transport.RoundTrip(request)
	if err != nil {
		code := http.StatusInternalServerError
		if res != nil && res.StatusCode != 0 {
			code = res.StatusCode
		}

		httperror.WriteError(rw, code, "Unable to proxy the request via the Docker socket", err)

		return
	}

	defer func() {
		if err := res.Body.Close(); err != nil {
			log.Warn().Err(err).Msg("Failed closing proxied request body")
		}
	}()

	for k, vv := range res.Header {
		for _, v := range vv {
			rw.Header().Add(k, v)
		}
	}

	rw.WriteHeader(res.StatusCode)

	// TODO: resource duplication error: it seems that the body size is different here
	// from the size retrieve in cluster.go
	if _, err := io.Copy(rw, res.Body); err != nil {
		log.Warn().Err(err).Msg("Error copying response body from proxied request")
	}
}
