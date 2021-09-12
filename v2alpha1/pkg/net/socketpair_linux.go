// +build linux

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

package net

import (
	"fmt"
	"net"
	"os"
	"syscall"

	"github.com/pkg/errors"
)

const (
	local = 0
	peer = 1
)

// SocketPair contains the file descriptors of a connected pair of sockets.
type SocketPair [2]int

// NewSocketPair returns a connected pair of sockets.
func NewSocketPair() (SocketPair, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return [2]int{-1,-1}, errors.Wrapf(err, "failed to create socketpair")
	}

	return fds, nil
}

// Local returns the socketpair fd for local usage.
func (fds SocketPair) Local() int {
	return fds[local]
}

// Peer returns the socketpair fd for peer usage.
func (fds SocketPair) Peer() int {
	return fds[peer]
}

// LocalFile returns the socketpair fd for local usage as an *os.File.
func (fds SocketPair) LocalFile() *os.File {
	return os.NewFile(uintptr(fds.Local()), fds.fileName()+"[0]")
}

// PeerFile returns the socketpair fd for peer usage as an *os.File.
func (fds SocketPair) PeerFile() *os.File {
	return os.NewFile(uintptr(fds.Peer()), fds.fileName()+"[1]")
}

// LocalConn returns a net.Conn for the local end of the socketpair.
func (fds SocketPair) LocalConn() (net.Conn, error) {
	file := fds.LocalFile()
	defer file.Close()
	conn, err := net.FileConn(file)
	if err != nil {
		return nil, errors.Wrapf(err,
			"failed to create net.Conn for %s[0]", fds.fileName())
	}
	return conn, nil
}

// PeerConn returns a net.Conn for the peer end of the socketpair.
func (fds SocketPair) PeerConn() (net.Conn, error) {
	file := fds.PeerFile()
	defer file.Close()
	conn, err := net.FileConn(file)
	if err != nil {
		return nil, errors.Wrapf(err,
			"failed to create net.Conn for %s[1]", fds.fileName())
	}
	return conn, nil
}

// LocalClose closes the local end of the socketpair.
func (fds SocketPair) LocalClose() {
	syscall.Close(fds[local])
}

// PeerClose closes the peer end of the socketpair.
func (fds SocketPair) PeerClose() {
	syscall.Close(fds[peer])
}

// Close closes both ends of the socketpair.
func (fds SocketPair) Close() {
	syscall.Close(fds[0])
	syscall.Close(fds[1])
}

func (fds SocketPair) fileName() string {
	return fmt.Sprintf("socketpair-#%d:%d[0]", fds[local], fds[peer])
}
