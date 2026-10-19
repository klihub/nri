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

package suite

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/containerd/nri/test/nri-test/driver/cri"
	"github.com/containerd/nri/test/nri-test/framework"
)

func init() {
	framework.RegisterFlags()
}

func newCRIDriver(ctx context.Context, cfg *framework.Config) (framework.Driver, framework.Inspector, error) {
	d, i, err := cri.NewDriver(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	return d, i, nil
}

func TestMain(m *testing.M) {
	flag.Parse()

	if err := framework.TestConfig.Complete(); err != nil {
		fmt.Fprintf(os.Stderr, "nri-test: %v\n", err)
		os.Exit(1)
	}

	if err := framework.Setup(framework.TestConfig, newCRIDriver); err != nil {
		fmt.Fprintf(os.Stderr, "nri-test: setup failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("nri-test: runtime %s %s, NRI %s, run id %s\n",
		framework.RuntimeName(), framework.RuntimeVersion(),
		framework.RuntimeNRIVersion(), framework.TestConfig.RunID)

	code := m.Run()

	framework.Teardown()
	os.Exit(code)
}
