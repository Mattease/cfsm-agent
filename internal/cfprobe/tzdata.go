package cfprobe

// The probe runs in an Alpine container without system tzdata, so IANA
// timezone names (e.g. America/Los_Angeles) would fail to load. Embedding
// the timezone database here makes time.LoadLocation work everywhere this
// package is linked — including the go test binary, which is why the import
// lives in this package and not only in cmd/cf-probe/main.go.
import _ "time/tzdata"
