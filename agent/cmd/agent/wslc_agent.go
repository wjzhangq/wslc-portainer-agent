package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/portainer/agent"
	"github.com/portainer/agent/crypto"
	"github.com/portainer/agent/ghw"
	"github.com/portainer/agent/http"
	"github.com/portainer/agent/wslc"
	"github.com/rs/zerolog/log"
)

func runWslcAgent(options *agent.Options) error {
	ran, err := runAsWindowsService(func(ctx context.Context) error {
		return runWslc(ctx, options)
	})
	if err != nil {
		return err
	}
	if ran {
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runWslc(ctx, options)
}

func runWslc(ctx context.Context, options *agent.Options) error {
	dir, err := wslcWorkDir()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	if err := openWslcLog(dir); err != nil {
		return err
	}
	if options.SharedSecret == "" {
		return errors.New("AGENT_SECRET is required when WSLC=1")
	}

	hostName, err := os.Hostname()
	if err != nil {
		return err
	}
	runtimeConfiguration := &agent.RuntimeConfig{
		AgentPort: options.AgentServerPort,
		NodeName:  hostName,
		DockerConfig: agent.DockerRuntimeConfig{
			EngineType: agent.EngineTypeStandalone,
		},
	}

	certHost := certificateHost(options.AgentServerAddr)
	log.Info().
		Str("workdir", dir).
		Str("cert_host", certHost).
		Str("node", hostName).
		Str("listen", options.AgentServerAddr+":"+options.AgentServerPort).
		Msg("starting agent with wslc backend")

	tlsService := crypto.TLSService{}
	if err := tlsService.GenerateCertsForHost(certHost); err != nil {
		return err
	}
	probeWslcSession(ctx)

	return http.NewAPIServer(&http.APIServerConfig{
		Addr:                 options.AgentServerAddr,
		Port:                 options.AgentServerPort,
		SystemService:        ghw.NewSystemService(agent.HostRoot),
		SignatureService:     crypto.NewECDSAService(options.SharedSecret),
		RuntimeConfiguration: runtimeConfiguration,
		AgentOptions:         options,
		ContainerPlatform:    agent.PlatformDocker,
	}).StartContext(ctx, false)
}

func wslcWorkDir() (string, error) {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	dir := filepath.Join(base, "PortainerAgentWslc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func openWslcLog(dir string) error {
	file, err := os.OpenFile(filepath.Join(dir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	log.Logger = log.Output(io.MultiWriter(os.Stderr, file))
	return nil
}

func probeWslcSession(ctx context.Context) {
	exec, err := wslc.NewExecutor()
	if err != nil {
		log.Error().Err(err).Str("runtime", agentRuntime()).Msg("wslc ps probe skipped")
		return
	}
	stdout, stderr, code, err := exec.Run(ctx, "ps", "-a", "--format", "json", "--no-trunc")
	count := 0
	if err == nil && code == 0 {
		if rows, decErr := wslc.DecodeNDJSONBytes([]byte(stdout)); decErr == nil {
			count = len(rows)
		}
	}
	ev := log.Info()
	if err != nil || code != 0 {
		ev = log.Error().Err(err)
	}
	ev.Str("runtime", agentRuntime()).
		Int("exit_code", code).
		Int("containers", count).
		Str("stderr", strings.TrimSpace(stderr)).
		Msg("wslc ps probe")
}

func certificateHost(bind string) string {
	if ip := net.ParseIP(bind); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		return bind
	}
	if ip := firstNonLoopbackIPv4(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			return ip4.String()
		}
	}
	return ""
}
