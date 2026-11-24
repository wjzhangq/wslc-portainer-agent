package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/portainer/agent"
	"github.com/portainer/agent/crypto"
)

func main() {
	secret := flag.String("secret", os.Getenv("AGENT_SECRET"), "AGENT_SECRET")
	rawURL := flag.String("url", "", "request URL; omit to only self-check the signature")
	method := flag.String("method", http.MethodGet, "HTTP method")
	flag.Parse()

	if *secret == "" {
		fmt.Fprintln(os.Stderr, "AGENT_SECRET is required (-secret or AGENT_SECRET)")
		os.Exit(2)
	}

	pub, sig, err := Sign(*secret)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ok, err := crypto.NewECDSAService(*secret).VerifySignature(sig, pub)
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "self-check failed: ok=%v err=%v\n", ok, err)
		os.Exit(1)
	}
	if *rawURL == "" {
		fmt.Println("signature self-check ok")
		return
	}

	req, err := http.NewRequest(*method, *rawURL, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	req.Header.Set(agent.HTTPPublicKeyHeaderName, pub)
	req.Header.Set(agent.HTTPSignatureHeaderName, sig)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // agent certificate is self-signed
	}}
	res, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	fmt.Printf("%s %d\n", res.Status, res.StatusCode)
	os.Stdout.Write(body)
	if !strings.HasSuffix(string(body), "\n") {
		fmt.Println()
	}
	if res.StatusCode >= 400 {
		os.Exit(1)
	}
}
