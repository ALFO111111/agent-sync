//go:build bundled_host

package hostbundle

import _ "embed"

//go:embed generated/host.bin
var payload []byte

//go:embed generated/metadata.json
var metadata []byte
