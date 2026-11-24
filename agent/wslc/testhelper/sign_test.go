package main

import (
	"testing"

	"github.com/portainer/agent/crypto"
)

func TestSignAcceptedByAgent(t *testing.T) {
	const secret = "test-secret"
	pub, sig, err := Sign(secret)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := crypto.NewECDSAService(secret).VerifySignature(sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("signature was rejected")
	}

	ok, err = crypto.NewECDSAService("other-secret").VerifySignature(sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("signature was accepted for a different secret")
	}
}
