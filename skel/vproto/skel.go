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
	"encoding/json"
	"fmt"
	logger "log"
	"os"

	"github.com/pkg/errors"

	types "github.com/containerd/nri/types/vproto"
)

const (
	logPath = "/tmp/nri-skel-v2.log"
)

// Plugin is the interface plugins need to implement.
// Each method of the interface corresponds to a sandbox
// or container lifecycle event or state change.
type Plugin interface {
	// Type of the plugin, which is also the name of its binary.
	Type() string
	// Version of the plugin.
	Version() string
	// Configure is used by the skeleton to set the plugin configuration.
	Configure(context.Context, *types.ConfigureRequest) (*types.ConfigureResponse, error)
	// CreateSandbox is a sandbox creation request.
	CreateSandbox(context.Context, *types.CreateSandboxRequest) (*types.CreateSandboxResponse, error)
	// RemoveSandbox is a sandbox creation request.
	RemoveSandbox(context.Context, *types.RemoveSandboxRequest) (*types.RemoveSandboxResponse, error)
}

// Logger for plugin execution.
var log *logger.Logger

func Logger() *logger.Logger {
	return log
}

// Run runs one request/response exchange of the plugin.
func Run(ctx context.Context, plugin Plugin) error {
	var (
		request types.AnyRequest
		reply   types.AnyResponse
	)

	flags := os.O_CREATE|os.O_APPEND|os.O_WRONLY
	logFile, err := os.OpenFile(logPath, flags, 0644)
	if err != nil {
		return errors.Wrapf(err, "failed to open skeleton log file %q", logPath)
	}
	defer logFile.Close()

	log = logger.New(logFile, "skel-v2: ", 0)
	log.Printf("starting up...")

	if len(os.Args) < 2 {
		return fmt.Errorf("invalid plugin command line, missing action")
	}

	log.Printf("request action: %q", os.Args[1])
	log.Printf("decoding request...")
	if err = json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		return errors.Wrapf(err, "failed to decode request")
	}
	log.Printf("decoding request OK...")

	if request.Config != nil {
		log.Printf("configuring plugin...")
		req := request.Config
		rpl, err := plugin.Configure(ctx, req)
		reply.Config = rpl
		if err != nil {
			log.Printf("configuring failed: %v", err)
			reply.Error = err.Error()
		}
	}

	if err == nil {
		action := os.Args[1]
		switch types.Action(action) {
		case types.CreateSandbox:
			log.Printf("CreateSandbox()...")
			req := request.CreateSandbox
			rpl, err := plugin.CreateSandbox(ctx, req)
			reply.CreateSandbox = rpl
			if err != nil {
				log.Printf("CreateSandbox failed: %v", err)
				reply.Error = err.Error()
			} else {
				log.Printf("CreateSandbox OK")
			}
		case types.RemoveSandbox:
			log.Printf("RemoveSandbox()...", err)
			req := request.RemoveSandbox
			rpl, err := plugin.RemoveSandbox(ctx, req)
			reply.RemoveSandbox = rpl
			if err != nil {
				log.Printf("RemoveSandbox failed: %v", err)
				reply.Error = err.Error()
			} else {
				log.Printf("RemoveSandbox OK")
			}
		default:
			log.Printf("invalid action %q", action)
			reply.Error = fmt.Sprintf("invalid plugin action %q", action)
		}
	}

	if err = json.NewEncoder(os.Stdout).Encode(reply); err != nil {
		return errors.Wrapf(err, "unable to encode plugin response")
	}

	return nil
}

