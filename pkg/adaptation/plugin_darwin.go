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

package adaptation

import (
	"errors"
	"fmt"
	stdnet "net"

	"golang.org/x/sys/unix"
)

const (
	SOL_LOCAL     = 0
	LOCAL_PEERPID = 2
)

// getPeerPid returns the process id at the other end of the connection.
func getPeerPid(conn stdnet.Conn) (int, error) {
	var pid int

	uc, ok := conn.(*stdnet.UnixConn)
	if !ok {
		return 0, errors.New("invalid connection, not *net.UnixConn")
	}

	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("failed get raw unix domain connection: %w", err)
	}

	ctrlErr := raw.Control(func(fd uintptr) {
		pid, _ = unix.GetsockoptInt(int(fd), SOL_LOCAL, LOCAL_PEERPID)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get process credentials: %w", err)
	}
	if ctrlErr != nil {
		return 0, fmt.Errorf("uc.SyscallConn().Control() failed: %w", ctrlErr)
	}

	return pid, nil
}
