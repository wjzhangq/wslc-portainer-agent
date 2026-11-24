//go:build windows

package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows/svc"
)

func agentRuntime() string {
	isService, err := svc.IsWindowsService()
	if err == nil && isService {
		return "service"
	}
	return "console"
}

func runAsWindowsService(run func(context.Context) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	err = svc.Run("PortainerAgentWslc", &serviceHandler{run: run})
	return true, err
}

type serviceHandler struct {
	run func(context.Context) error
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- h.run(ctx)
	}()

	status <- svc.Status{State: svc.StartPending}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error().Err(err).Msg("wslc agent stopped")
				return false, 1
			}
			return false, 0
		}
	}
}
