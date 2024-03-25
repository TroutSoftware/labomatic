package labomatic

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

var identity string

func gensshkeypair() ([]byte, error) {
	if identity != "" {
		if _, err := os.Stat(identity); err == nil {
			pub, err := os.ReadFile(identity + ".pub")
			return pub[:len(pub)-1], err
		}

	}

	var err error
	identity, err = os.MkdirTemp("", "keymat")
	if err != nil {
		return nil, fmt.Errorf("cannot create temp dir: %w", err)
	}

	identity = filepath.Join(identity, "id_labomatic")
	pub, pid, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	content, err := ssh.MarshalPrivateKey(pid, "labomatic")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(identity, pem.EncodeToMemory(content), 0600); err != nil {
		return nil, fmt.Errorf("cannot persist SSH key: %w", err)
	}

	spk, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}

	mk := ssh.MarshalAuthorizedKey(spk)

	if err := os.WriteFile(identity+".pub", mk, 0644); err != nil {
		return nil, fmt.Errorf("cannot persist SSH public key: %w", err)
	}

	return mk[:len(mk)-1], nil
}
