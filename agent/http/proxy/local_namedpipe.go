//go:build windows

package proxy

import (
	"net"
	"net/http"
	"os"

	"github.com/Microsoft/go-winio"
	"github.com/rs/zerolog/log"
)

// NewLocalProxy returns a pointer to a LocalProxy.
func NewLocalProxy() *LocalProxy {
	if os.Getenv("WSLC") == "1" {
		proxy, err := newWslcProxy()
		if err != nil {
			log.Fatal().Err(err).Msg("unable to initialize wslc backend")
		}
		return proxy
	}
	return &LocalProxy{
		transport: newNamedPipeTransport("//./pipe/docker_engine"),
	}
}

func newNamedPipeTransport(namedPipePath string) *http.Transport {
	return &http.Transport{
		Dial: func(proto, addr string) (conn net.Conn, err error) {
			return winio.DialPipe(namedPipePath, nil)
		},
	}
}
