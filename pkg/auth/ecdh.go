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

package auth

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	fmt "fmt"
	"slices"

	chacha "golang.org/x/crypto/chacha20poly1305"
)

var (
	// ErrInvalidKey indicates failures to encode, decode or use a key.
	ErrInvalidKey = errors.New("invalid key")
	// ErrChallenge indicate failure to generate or verify challenge.
	ErrChallenge = errors.New("challenge failed")
	// ErrAuth indicates failed authentication.
	ErrAuthFailed = errors.New("authentication failed")
)

func GenerateKeyPair() (*PrivateKey, *PublicKey, error) {
	privK, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	priv := &PrivateKey{
		PrivateKey: privK,
	}
	pub := &PublicKey{
		PublicKey: privK.Public().(*ecdh.PublicKey),
	}

	return priv, pub, nil
}

type PrivateKey struct {
	*ecdh.PrivateKey
	bytes     []byte
	secret    []byte
	challenge []byte
}

func DecodePrivateKey(bytes []byte) (*PrivateKey, error) {
	k := &PrivateKey{}
	if err := k.Decode(bytes); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *PrivateKey) Clear() {
	if k == nil {
		return
	}

	k.PrivateKey = nil

	for i := range k.bytes {
		k.bytes[i] = 0
	}
	for i := range k.secret {
		k.secret[i] = 0
	}
	for i := range k.challenge {
		k.challenge[i] = 0
	}
}

func (k *PrivateKey) Encode() []byte {
	bytes := make([]byte, base64.StdEncoding.EncodedLen(len(k.Bytes())))
	base64.StdEncoding.Encode(bytes, k.Bytes())
	k.bytes = bytes

	return k.bytes
}

func (k *PrivateKey) Decode(bytes []byte) error {
	data := make([]byte, base64.StdEncoding.DecodedLen(len(bytes)))
	n, err := base64.StdEncoding.Decode(data, bytes)
	if err != nil {
		return fmt.Errorf("%w: failed to decode private key: %w", ErrInvalidKey, err)
	}

	key, err := ecdh.X25519().NewPrivateKey(data[:n])
	if err != nil {
		return fmt.Errorf("%w: failed to decode private key: %w", ErrInvalidKey, err)
	}

	k.PrivateKey = key

	return nil
}

func (k *PrivateKey) SharedSecret(peer *PublicKey) ([]byte, error) {
	if len(k.secret) != 0 {
		return slices.Clone(k.secret), nil
	}

	secret, err := k.ECDH(peer.PublicKey)
	if err != nil {
		return nil, err
	}

	if len(secret) > chacha.KeySize {
		return secret[:chacha.KeySize], nil
	}

	k.secret = secret

	return k.secret, nil
}

func (k *PrivateKey) Seal(peer *PublicKey, clear []byte) ([]byte, error) {
	key, err := k.SharedSecret(peer)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	aead, err := chacha.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	return aead.Seal(nonce, nonce, clear, nil), nil
}

func (k *PrivateKey) Open(peer *PublicKey, cipher []byte) ([]byte, error) {
	key, err := k.SharedSecret(peer)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	aead, err := chacha.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	nonceSize := aead.NonceSize()
	if l := len(cipher); l < nonceSize {
		return nil, fmt.Errorf("%w: challenge too short (%d < %d)", ErrChallenge, l, nonceSize)
	}

	nonce, cipher := cipher[:nonceSize], cipher[nonceSize:]

	clear, err := aead.Open(nil, nonce, cipher, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	return clear, nil
}

func (k *PrivateKey) GenerateChallenge(peer *PublicKey) ([]byte, error) {
	k.challenge = make([]byte, 32)

	_, err := rand.Read(k.challenge)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	return k.Seal(peer, k.challenge)
}

func (k *PrivateKey) GenerateResponse(peer *PublicKey, cipher []byte) ([]byte, error) {
	challenge, err := k.Open(peer, cipher)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	response := sha256.Sum256(challenge)

	return k.Seal(peer, response[:])
}

func (k *PrivateKey) VerifyResponse(peer *PublicKey, cipher []byte) error {
	response, err := k.Open(peer, cipher)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	sum := sha256.Sum256(k.challenge)
	if !slices.Equal(response, sum[:]) {
		return fmt.Errorf("%w: incorrect response", ErrAuthFailed)
	}

	return nil
}

type PublicKey struct {
	*ecdh.PublicKey
	bytes  []byte
	secret []byte
}

func DecodePublicKey(bytes []byte) (*PublicKey, error) {
	k := &PublicKey{}
	if err := k.Decode(bytes); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *PublicKey) Clear() {
	if k == nil {
		return
	}

	k.PublicKey = nil

	for i := range k.bytes {
		k.bytes[i] = 0
	}
	for i := range k.secret {
		k.secret[i] = 0
	}
}

func (k *PublicKey) Encode() []byte {
	bytes := make([]byte, base64.StdEncoding.EncodedLen(len(k.Bytes())))
	base64.StdEncoding.Encode(bytes, k.Bytes())
	k.bytes = bytes

	return k.bytes
}

func (k *PublicKey) Decode(bytes []byte) error {
	data := make([]byte, base64.StdEncoding.DecodedLen(len(bytes)))
	n, err := base64.StdEncoding.Decode(data, bytes)
	if err != nil {
		return fmt.Errorf("%w: failed to decode public key: %w", ErrInvalidKey, err)
	}

	key, err := ecdh.X25519().NewPublicKey(data[:n])
	if err != nil {
		return fmt.Errorf("%w: failed to decode public key: %w", ErrInvalidKey, err)
	}

	k.PublicKey = key

	return nil
}
