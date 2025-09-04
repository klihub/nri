/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package ecdh

import (
	"crypto/sha256"
	fmt "fmt"
	"slices"

	"github.com/containerd/nri/pkg/auth/crypto"
)

type Algorithm struct {
	priv *PrivateKey
	pub  *PublicKey
	peer *PublicKey
	seed []byte
}

var _ crypto.Algorithm = &Algorithm{}

func NewWithKeyPair(priv crypto.PrivateKey, pub crypto.PublicKey) (*Algorithm, error) {
	privKey, err := DecodePrivateKey(priv)
	if err != nil {
		return nil, err
	}

	pubKey, err := DecodePublicKey(pub)
	if err != nil {
		return nil, err
	}

	return &Algorithm{
		priv: privKey,
		pub:  pubKey,
	}, nil
}

func NewWithTmpKeyPair() (*Algorithm, error) {
	privKey, pubKey, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	return &Algorithm{
		priv: privKey,
		pub:  pubKey,
	}, nil
}

func (a *Algorithm) PrivateKey() crypto.PrivateKey {
	return a.priv.Encode()
}

func (a *Algorithm) PublicKey() crypto.PublicKey {
	return a.pub.Encode()
}

func (a *Algorithm) Challenge(seed []byte, peer crypto.PublicKey) ([]byte, crypto.PublicKey, error) {
	peerKey, err := DecodePublicKey(peer)
	if err != nil {
		return nil, nil, err
	}

	a.peer = peerKey
	a.seed = seed

	challenge, err := a.priv.Seal(a.peer, seed)
	if err != nil {
		return nil, nil, err
	}

	return challenge, crypto.PublicKey(a.pub.Encode()), nil
}

func (a *Algorithm) Response(challenge []byte, peer crypto.PublicKey) ([]byte, error) {
	peerKey, err := DecodePublicKey(peer)
	if err != nil {
		return nil, err
	}

	a.peer = peerKey

	seed, err := a.priv.Open(a.peer, challenge)
	if err != nil {
		return nil, err
	}

	response := sha256.Sum256(seed)

	return a.priv.Seal(a.peer, response[:])
}

func (a *Algorithm) Verify(cipher []byte) error {
	response, err := a.priv.Open(a.peer, cipher)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	sum := sha256.Sum256(a.seed)
	if !slices.Equal(response, sum[:]) {
		return fmt.Errorf("%w: incorrect response", ErrAuthFailed)
	}

	return nil
}
