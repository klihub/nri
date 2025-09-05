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
	"bytes"
	"fmt"
	"os"

	"github.com/containerd/nri/pkg/auth/crypto"
)

// FileFetcher fetches authentication keys from a file.
type FileFetcher struct {
	path string
	data [][]byte
}

// NewFileFetcher returns an AuthKeyFetcher for the given file.
func NewFileFetcher(path string) *FileFetcher {
	return &FileFetcher{path: path}
}

// ClearKeys any cached key data.
func (f *FileFetcher) ClearKeys() {
	for _, bytes := range f.data {
		for i := range bytes {
			bytes[i] = 0
		}
	}
	f.data = nil
}

// FetchKeys fetches the private key and public keys (line 1 and 2) from a file.
func (f *FileFetcher) FetchKeys() (crypto.PrivateKey, crypto.PublicKey, error) {
	if err := f.read(); err != nil {
		return nil, nil, err
	}

	return crypto.PrivateKey(f.data[0]), crypto.PublicKey(f.data[1]), nil
}

func (f *FileFetcher) read() error {
	if f.data == nil {
		data, err := os.ReadFile(f.path)
		if err != nil {
			return fmt.Errorf("failed get public key: %w", err)
		}
		split := bytes.SplitAfter(data, []byte("\n"))
		if len(split) < 2 {
			return fmt.Errorf("need file with at least 2 lines: private key and public key")
		}
		f.data = split[:2]
	}

	return nil
}

// InMemoryKeyFetcher returns keys from memory.
type InMemoryKeyFetcher struct {
	priv []byte
	pub  []byte
}

// NewInMemoryKeyFetcher returns an AuthKeyFetcher for the given keys.
func NewInMemoryKeyFetcher(priv, pub []byte) *InMemoryKeyFetcher {
	return &InMemoryKeyFetcher{priv: priv, pub: pub}
}

// FetchKeys returns the private and public keys from memory.
func (f *InMemoryKeyFetcher) FetchKeys() (crypto.PrivateKey, crypto.PublicKey, error) {
	return crypto.PrivateKey(f.priv), crypto.PublicKey(f.pub), nil
}

// ClearKeys clears the keys from memory.
func (f *InMemoryKeyFetcher) ClearKeys() {
	for i := range f.priv {
		f.priv[i] = 0
	}
	for i := range f.pub {
		f.pub[i] = 0
	}
	f.priv = nil
	f.pub = nil
}
