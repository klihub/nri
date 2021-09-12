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

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// Logging is an interface for creating loggers and writing log messages.
type Logging interface {
	Get(string) Logger
	message(string, ...interface{})
	block(string, string, ...interface{})
	SetWriter(io.Writer)
}

// Logger is an interface for logging.
type Logger interface {
	Debug(string, ...interface{})
	Info(string, ...interface{})
	Warn(string, ...interface{})
	Error(string, ...interface{})
	Fatal(string, ...interface{})
	Panic(string, ...interface{})
	DebugBlock(string, string, ...interface{})
	InfoBlock(string, string, ...interface{})
	WarnBlock(string, string, ...interface{})
	ErrorBlock(string, string, ...interface{})
	SetWriter(io.Writer)
}

type logging struct {
	w io.Writer
}

type logger struct {
	log    *logging
	prefix string
}

func LogWriter(w io.Writer) Logging {
	return &logging{
		w: w,
	}
}

func (l *logging) Get(prefix string) Logger {
	return &logger{
		log:    l,
		prefix: "[" + prefix + "] ",
	}
}

func (l *logging) message(format string, args ...interface{}) {
	buf := bytes.Buffer{}
	buf.WriteString(fmt.Sprintf(format+"\n", args...))
	l.w.Write(buf.Bytes())
}

func (l *logging) block(prefix, format string, args ...interface{}) {
	buf := bytes.Buffer{}
	for _, line := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		buf.WriteString(prefix)
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	l.w.Write(buf.Bytes())
}

func (l *logging) SetWriter(w io.Writer) {
	l.w = w
}

func (l *logger) Debug(format string, args ...interface{}) {
	l.log.message("D: "+l.prefix+format, args...)
}

func (l *logger) Info(format string, args ...interface{}) {
	l.log.message("I: "+l.prefix+format, args...)
}

func (l *logger) Warn(format string, args ...interface{}) {
	l.log.message("W: "+l.prefix+format, args...)
}

func (l *logger) Error(format string, args ...interface{}) {
	l.log.message("E: "+l.prefix+format, args...)
}

func (l *logger) Fatal(format string, args ...interface{}) {
	l.log.message("Fatal Error: "+l.prefix+format, args...)
	os.Exit(1)
}

func (l *logger) Panic(format string, args ...interface{}) {
	l.log.message("PANIC: "+l.prefix+format, args...)
	panic(fmt.Sprintf(l.prefix+format, args...))
}

func (l *logger) DebugBlock(prefix, format string, args ...interface{}) {
	l.log.block("D: "+l.prefix+prefix, format, args...)
}

func (l *logger) InfoBlock(prefix, format string, args ...interface{}) {
	l.log.block("I: "+l.prefix+prefix, format, args...)
}

func (l *logger) WarnBlock(prefix, format string, args ...interface{}) {
	l.log.block("W: "+l.prefix+prefix, format, args...)
}

func (l *logger) ErrorBlock(prefix, format string, args ...interface{}) {
	l.log.block("E: "+l.prefix+prefix, format, args...)
}

func (l *logger) SetWriter(w io.Writer) {
	l.log.SetWriter(w)
}

