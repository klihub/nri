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

package multiplex

import (
	"fmt"
	stdnet "net"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/containerd/nri/v2alpha1/pkg/net"
)

type testBatch struct {
	t        *testing.T
	name     string
	start    <-chan struct{}
	done     *sync.WaitGroup
	mux      Mux
	connID   ConnID
	conn     stdnet.Conn
	messages []string
	err      error
}

func TestSingleConnection(t *testing.T) {
	sMux, cMux, err := getConnectedMuxPair()
	if err != nil {
		t.Errorf("failed to open Mux: %v", err)
		return
	}

	connID := LowestConnID
	sConn, err := sMux.Open(connID)
	if err != nil {
		t.Errorf("failed to open connection %d: %v", connID, err)
		return
	}
	cConn, err := cMux.Open(connID)
	if err != nil {
		t.Errorf("failed to open connection %d: %v", connID, err)
		return
	}

	start := make(chan struct{})
	done := &sync.WaitGroup{}
	batchSize := 8192

	w := newBatch("single-conn", t, start, done, sMux, 0, sConn)
	r := newBatch("single-conn", t, start, done, cMux, 0, cConn)

	if err := w.write(batchSize); err != nil {
		t.Errorf("%v", err)
		return
	}
	if err := r.read(); err != nil {
		t.Errorf("%v", err)
		return
	}

	done.Add(2)
	close(start)
	done.Wait()

	if len(w.messages) != len(r.messages) {
		t.Errorf("sent %d message but received %d",
			len(w.messages), len(r.messages))
		return
	}

	for idx, sent := range w.messages {
		t.Logf("verifying message #%d (%q)", idx, sent)
		if received := r.messages[idx]; received != sent {
			t.Errorf("#%d message sent was %q, but read %q",
				idx, sent, received)
		}
	}
}

func TestMultipleConnections(t *testing.T) {
	sMux, cMux, err := getConnectedMuxPair()
	if err != nil {
		t.Errorf("failed to open Mux: %v", err)
		return
	}


	start := make(chan struct{})
	done := &sync.WaitGroup{}

	batchSize := 1024
	batchCount := 16
	writers := []*testBatch{}
	readers := []*testBatch{}
	connID := LowestConnID
	for i := 0; i < batchCount; i++ {
		name := fmt.Sprintf("multiple-conn#%d", i)
		w := newBatch(name, t, start, done, sMux, connID, nil)
		r := newBatch(name, t, start, done, cMux, connID, nil)

		if err := w.write(batchSize); err != nil {
			t.Errorf("%v", err)
			return
		}
		if err := r.read(); err != nil {
			t.Errorf("%v", err)
			return
		}

		writers = append(writers, w)
		readers = append(readers, r)

		connID++
	}


	done.Add(len(writers) + len(readers))
	close(start)
	done.Wait()

	for i, w := range writers {
		r := readers[i]
		if len(w.messages) != len(r.messages) {
			t.Errorf("sent %d message but received %d",
				len(w.messages), len(r.messages))
			return
		}

		for idx, sent := range w.messages {
			t.Logf("verifying message #%d (%q)", idx, sent)
			if received := r.messages[idx]; received != sent {
				t.Errorf("#%d message sent was %q, but read %q",
					idx, sent, received)
			}
		}
	}
}

func TestParallelConnections(t *testing.T) {
	sMux, _, _ := getConnectedMuxPair()
	sMux.(*mux).trunk.Close()
	sMux.(*mux).trunk.Close()
}

func TestForbiddenReserved(t *testing.T) {
	sMux, cMux, err := getConnectedMuxPair()
	if err != nil {
		t.Errorf("failed to open Mux: %v", err)
		return
	}

	_, err = sMux.Open(ConnID(reservedConnID))
	if err == nil {
		t.Errorf("opening ConnID(#%d) should have failed", reservedConnID)
	}

	sMux.Close()
	cMux.Close()
}

func getConnectedMuxPair() (Mux, Mux, error) {
	sSock, cSock, err := net.NewSocketPair()
	if err != nil {
		return nil, nil, err
	}

	sConn, err := stdnet.FileConn(sSock)
	if err != nil {
		return nil, nil, err
	}
	cConn, err := stdnet.FileConn(cSock)
	if err != nil {
		return nil, nil, err
	}

	return Multiplex(sConn), Multiplex(cConn), nil
}

const (
	doneMessage = "done"
)

func newBatch(name string, t *testing.T, start <-chan struct{}, done *sync.WaitGroup, mux Mux, connID ConnID, conn stdnet.Conn) *testBatch {
	return &testBatch{
		name:   name,
		t:      t,
		start:  start,
		done:   done,
		mux:    mux,
		connID: connID,
		conn:   conn,
	}
}

func (b *testBatch) write(msgCount int) error {
	var (
		msg string
		err error
	)

	if b.conn == nil {
		b.conn, err = b.mux.Open(b.connID)
		if err != nil {
			return errors.Wrapf(err,
				"%s-writer: failed to open mux connection %d", b.name, b.connID)
		}
	}

	go func() {
		if b.start != nil {
			_ = <- b.start
		}

		b.messages = make([]string, 0, msgCount)

		for i := 0; i < msgCount; i++ {
			if i < msgCount - 1 {
				msg = fmt.Sprintf("%s message #%d", b.name, i)
			} else {
				msg = doneMessage
			}

			b.t.Logf("%s-writer: sending #%d message %q", b.name, i, msg)

			_, err = b.conn.Write([]byte(msg))
			if err != nil {
				b.err = errors.Wrapf(err,
					"%s-writer: failed to write message #%d ", b.name, i)
				break
			}

			b.messages = append(b.messages, msg)

			if (i+1) % (10*readQueueLen) != 0 {
				time.Sleep(25 * time.Microsecond)
			}
		}

		if b.done != nil {
			b.done.Done()
		}
	}()

	return nil
}

func (b *testBatch) read() error {
	var (
		buf [1024]byte
		msg string
		err error
	)

	if b.conn == nil {
		b.conn, err = b.mux.Open(b.connID)
		if err != nil {
			return errors.Wrapf(err,
				"%s-reader: failed to open mux connection %d", b.name, b.connID)
		}
	}

	go func() {
		if b.start != nil {
			_ = <- b.start
		}

		for i := 0; ; i++ {
			b.t.Logf("%s-reader: waiting for #%d message...", b.name, i)

			cnt, err := b.conn.Read(buf[:])
			if err != nil {
				b.err = errors.Wrapf(err,
					"%s-reader: failed to read message #%d", b.name, i)
				break
			}

			msg = string(buf[:cnt])
			b.messages = append(b.messages, msg)

			b.t.Logf("%s-reader: got #%d message %q", b.name, i, msg)

			if msg == doneMessage {
				break
			}
		}

		if b.done != nil {
			b.done.Done()
		}
	}()

	return nil
}
