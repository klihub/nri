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

package vproto

import (
	"context"
	"github.com/sirupsen/logrus"
)

// Logging is the interface we use to retrieve loggers.
type Logging interface {
	// Context returns the logger for/from the given context.
	Context(context.Context) Logger
	// Default returns the default logger.
	Default() Logger
}

// Logger is the interface we use for logging messages.
type Logger interface {
	// Debug logs a debug message.
	Debug(args ...interface{})
	// Debugf logs a formatted debug message.
	Debugf(format string, args ...interface{})
	// Debug logs a debug message.
	Debugln(args ...interface{})

	// Info logs an informational message.
	Info(args ...interface{})
	// Infof logs a formatted informational message.
	Infof(format string, args ...interface{})
	// Infoln logs an information message.
	Infoln(args ...interface{})

	// Warn logs a warning message.
	Warn(args ...interface{})
	// Warnf logs a formatted warning message.
	Warnf(format string, args ...interface{})
	// Warnln logs a warning message.
	Warnln(args ...interface{})

	// Error logs an error message.
	Error(args ...interface{})
	// Errorf logs a formatted error message.
	Errorf(format string, args ...interface{})
	// Errorln logs an error message.
	Errorln(args ...interface{})
}

var (
	logging Logging
	log     Logger
)

// SetLogging sets up the NRI logging interface.
func SetLogging(l Logging) {
	logging = l
	log = l.Default()
}

type fallbackLogging struct {
	log Logger
}

func (d *fallbackLogging) Context(context.Context) Logger {
	return d.log
}

func (d *fallbackLogging) Default() Logger {
	return d.log
}

func init() {
	logging = &fallbackLogging{
		log: logrus.NewEntry(logrus.StandardLogger()),
	}
	log = logging.Default()
}
