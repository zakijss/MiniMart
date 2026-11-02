package main

import _ "embed"

// builtinPorts is the ports list as tools/catalog last wrote it into the
// source tree (tools/catalog -ports-out ... -ports-builtin
// cmd/minimart/ports_builtin.json), shown when the published one can't be
// read.
//
//go:embed ports_builtin.json
var builtinPorts []byte
