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

// Command runtime-status renders the runtime status document from the
// fragments of nri-test runs, or checks that the committed document matches.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"text/template"

	"github.com/containerd/nri/test/nri-test/status"
)

func main() {
	fragments := flag.String("fragments", "status/fragments", "directory of fragments")
	tmplPath := flag.String("template", "", "template file, default the embedded template")
	output := flag.String("output", "RUNTIME-STATUS.md", "document to write or check")
	check := flag.Bool("check", false, "compare the render with the document, writing nothing")
	force := flag.Bool("force", false,
		"write a render that records no measurement over the document that is there")
	flag.Parse()

	if err := run(*fragments, *tmplPath, *output, *check, *force); err != nil {
		fmt.Fprintf(os.Stderr, "runtime-status: %v\n", err)
		os.Exit(1)
	}
}

func run(fragments, tmplPath, output string, check, force bool) error {
	frags, err := status.Load(fragments)
	if err != nil {
		return err
	}

	view, err := status.Build(frags)
	if err != nil {
		return err
	}

	var tmpl *template.Template
	if tmplPath == "" {
		tmpl, err = status.DefaultTemplate()
	} else {
		tmpl, err = status.ParseTemplate(tmplPath)
	}
	if err != nil {
		return err
	}

	// Render into memory so a failing template leaves the document alone.
	var buf bytes.Buffer
	if err := status.Render(&buf, tmpl, view); err != nil {
		return err
	}

	if check {
		return status.Check(output, buf.Bytes())
	}

	return status.WriteDocument(output, buf.Bytes(), view, force)
}
