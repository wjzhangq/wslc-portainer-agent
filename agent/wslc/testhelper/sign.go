package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"math/big"
)

// Sign builds the two headers Portainer sends when AGENT_SECRET is set.
// The signed digest is MD5(secret), matching crypto.ECDSAService.
func Sign(secret string) (publicKeyHex, signature string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	sum := md5.Sum([]byte(secret))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", "", err
	}
	size := key.Curve.Params().BitSize / 8
	raw := append(padInt(r, size), padInt(s, size)...)
	return hex.EncodeToString(pub), base64.RawStdEncoding.EncodeToString(raw), nil
}

func padInt(n *big.Int, size int) []byte {
	b := n.Bytes()
	if len(b) >= size {
		return b[len(b)-size:]
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out
}
