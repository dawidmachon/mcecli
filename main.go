// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command mcecli is a lightweight, dependency-free CLI for operating Salesforce
// Marketing Cloud over its REST API. It is designed to be driven by local AI
// agents (and humans): one stable JSON envelope, self-describing commands,
// small responses, and hard gates on anything that writes.
package main

import (
	_ "embed"
	"os"

	"github.com/dawidmachon/mcecli/internal/cli"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)" -o mcecli.exe .
var version = "dev"

//go:embed SKILL.md
var skillMD string

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version, skillMD))
}
