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

	// Listing is static: it must not dial CRI, connect a plugin or pull an
	// image, or it would need root and a live runtime just to print names. So
	// this comes before Complete(), whose endpoint autodetection fails on a node
	// with no runtime socket -- exactly where listing must still work.
	if framework.ListingOnly() {
		os.Exit(m.Run())
	}

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

	// Teardown prints the run's summary, so it runs before the exit code is
	// decided and before the process leaves.
	//
	// It is handed m.Run's verdict, because the summary cannot derive it. Most of
	// this package asserts through bare require and t.Errorf calls, which record
	// nothing with the report, so a run that failed only through those would print
	// an empty summary that reads exactly like a clean one. With the code in hand
	// the summary says instead that it is blind and that the output above is the
	// only account of what failed.
	framework.Teardown(code)

	// A run that collected a failure must not exit 0. In collecting mode a
	// failure is reported through Errorf, so go test already fails the run and
	// this changes nothing; this is the belt for the case where a finding was
	// recorded but the test's own verdict somehow did not carry it, which is the
	// dropped-update defect one level up: the summary says it failed and the exit
	// code says it passed, and CI believes the exit code.
	if code == 0 && framework.TestReport.HasFindings() {
		code = 1
	}

	os.Exit(code)
}
