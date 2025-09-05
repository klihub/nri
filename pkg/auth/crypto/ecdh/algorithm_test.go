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

package ecdh_test

import (
	"crypto/rand"
	"testing"

	"github.com/containerd/nri/pkg/auth/crypto"
	"github.com/containerd/nri/pkg/auth/crypto/ecdh"
	"github.com/stretchr/testify/require"
)

type (
	Algorithm = ecdh.Algorithm
)

var (
	NewWithKeyPair    = ecdh.NewWithKeyPair
	NewWithTmpKeyPair = ecdh.NewWithTmpKeyPair
)

func TestECDHAuth(t *testing.T) {
	var (
		runtime   *Algorithm
		plugin    *Algorithm
		peer      crypto.PublicKey
		challenge []byte
		response  []byte
		err       error
	)

	t.Run("Set up runtime with a newly generated key pair", func(t *testing.T) {
		runtime, err = NewWithTmpKeyPair()
		require.NoError(t, err)
	})

	t.Run("Set up plugin with a pre-generated key pair", func(t *testing.T) {
		priv, pub, err := GenerateKeyPair()
		require.NoError(t, err)

		plugin, err = NewWithKeyPair(priv.Encode(), pub.Encode())
		require.NoError(t, err)
	})

	t.Run("Generate challenge at runtime", func(t *testing.T) {
		seed := make([]byte, 32)
		_, err = rand.Read(seed)
		require.NoError(t, err)

		challenge, peer, err = runtime.Challenge(seed, plugin.PublicKey())
		require.NoError(t, err)
	})

	t.Run("Generate response to challenge at plugin", func(t *testing.T) {
		response, err = plugin.Response(challenge, peer)
		require.NoError(t, err)
	})

	t.Run("Verify generated response at runtime", func(t *testing.T) {
		require.NoError(t, runtime.Verify(response))
	})
}
