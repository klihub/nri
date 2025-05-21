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

/*

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
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

type PrivateKey struct {
	*ecdsa.PrivateKey
	ecdh      *ecdh.PrivateKey
	bytes     []byte
	secret    []byte
	challenge []byte
}

type PublicKey struct {
	*ecdsa.PublicKey
	ecdh   *ecdh.PublicKey
	bytes  []byte
	secret []byte
}

func GenerateKeyPair() (*PrivateKey, *PublicKey, error) {
	privK, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	priv := &PrivateKey{
		PrivateKey: privK,
	}
	pub := &PublicKey{
		PublicKey: privK.Public().(*ecdsa.PublicKey),
	}

	if err := priv.prepare(); err != nil {
		return nil, nil, err
	}
	if err := pub.prepare(); err != nil {
		return nil, nil, err
	}

	return priv, pub, nil
}

func DecodePrivateKey(bytes []byte) (*PrivateKey, error) {
	k := &PrivateKey{}
	if err := k.Decode(bytes); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *PrivateKey) prepare() error {
	_, err := k.encode()
	if err != nil {
		return err
	}

	key, err := k.PrivateKey.ECDH()
	if err != nil {
		return err
	}
	k.ecdh = key

	return nil
}

func (k *PrivateKey) Clear() {
	if k == nil {
		return
	}

	k.PrivateKey = nil
	k.ecdh = nil

	for i := range k.bytes {
		k.bytes[i] = 0
	}
	for i := range k.challenge {
		k.challenge[i] = 0
	}
}

func (k *PrivateKey) Encode() []byte {
	return k.bytes
}

func (k *PrivateKey) ECDH() *ecdh.PrivateKey {
	return k.ecdh
}

func (k *PrivateKey) encode() ([]byte, error) {
	data, err := x509.MarshalPKCS8PrivateKey(k.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to encode private key: %w", ErrInvalidKey, err)
	}

	bytes := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
	base64.StdEncoding.Encode(bytes, data)

	k.bytes = bytes

	return k.bytes, nil
}

func (k *PrivateKey) Decode(bytes []byte) error {
	data := make([]byte, base64.StdEncoding.DecodedLen(len(bytes)))
	n, err := base64.StdEncoding.Decode(data, bytes)
	if err != nil {
		return fmt.Errorf("%w: failed to decode private key: %w", ErrInvalidKey, err)
	}

	anyKey, err := x509.ParsePKCS8PrivateKey(data[:n])
	if err != nil {
		return fmt.Errorf("%w: failed to decode private key: %w", ErrInvalidKey, err)
	}

	key, ok := anyKey.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("%w: unexpected private key type %T", ErrInvalidKey, anyKey)
	}

	k.PrivateKey = key

	return k.prepare()
}

func (k *PrivateKey) SharedSecret(peer *PublicKey) ([]byte, error) {
	if len(k.secret) != 0 {
		return slices.Clone(k.secret), nil
	}

	secret, err := k.ECDH().ECDH(peer.ECDH())
	if err != nil {
		return nil, err
	}
	if len(secret) > chacha.KeySize {
		return secret[:chacha.KeySize], nil
	}

	k.secret = secret

	return secret, nil
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

	response, err := ecdsa.SignASN1(rand.Reader, k.PrivateKey, challenge)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChallenge, err)
	}

	return response, nil
}

func (k *PrivateKey) VerifyResponse(peer *PublicKey, response []byte) error {
	if !ecdsa.VerifyASN1(peer.PublicKey, k.challenge, response) {
		return fmt.Errorf("%w: incorrect signature", ErrAuthFailed)
	}

	return nil
}

func DecodePublicKey(bytes []byte) (*PublicKey, error) {
	k := &PublicKey{}
	if err := k.Decode(bytes); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *PublicKey) prepare() error {
	_, err := k.encode()
	if err != nil {
		return err
	}

	key, err := k.PublicKey.ECDH()
	if err != nil {
		return err
	}
	k.ecdh = key

	return nil
}

func (k *PublicKey) Clear() {
	if k == nil {
		return
	}

	k.PublicKey = nil
	k.ecdh = nil

	for i := range k.bytes {
		k.bytes[i] = 0
	}
}

func (k *PublicKey) Encode() []byte {
	return k.bytes
}

func (k *PublicKey) ECDH() *ecdh.PublicKey {
	return k.ecdh
}

func (k *PublicKey) encode() ([]byte, error) {
	data, err := x509.MarshalPKIXPublicKey(k.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to encode public key: %w", ErrInvalidKey, err)
	}

	bytes := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
	base64.StdEncoding.Encode(bytes, data)

	k.bytes = bytes

	return k.bytes, nil
}

func (k *PublicKey) Decode(bytes []byte) error {
	data := make([]byte, base64.StdEncoding.DecodedLen(len(bytes)))
	n, err := base64.StdEncoding.Decode(data, bytes)
	if err != nil {
		return fmt.Errorf("%w: failed to decode public key: %w", ErrInvalidKey, err)
	}

	anyKey, err := x509.ParsePKIXPublicKey(data[:n])
	if err != nil {
		return fmt.Errorf("%w: failed to decode public key: %w", ErrInvalidKey, err)
	}

	key, ok := anyKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: unexpected public key type %T", ErrInvalidKey, anyKey)
	}

	k.PublicKey = key

	return k.prepare()
}

*/
