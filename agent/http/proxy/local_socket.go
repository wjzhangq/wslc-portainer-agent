//go:build !windows

package proxy

import (
	"net"
	"net/http"
	"os"

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
		transport: newSocketTransport("/var/run/docker.sock"),
	}
}

func newSocketTransport(socketPath string) *http.Transport {
	return &http.Transport{
		Dial: func(proto, addr string) (conn net.Conn, err error) {
			return net.Dial("unix", socketPath)
		},
	}
}
