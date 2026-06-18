module github.com/restrukt-ai/sessiongraphprotocol/examples/sgp-harness-math

go 1.26.4

require (
	github.com/cayleygraph/cayley v0.7.7
	github.com/hidal-go/hidalgo v0.0.0-20190814174001-42e03f3b5eaa
	github.com/restrukt-ai/openagentcontainers v0.0.0-00010101000000-000000000000
	github.com/restrukt-ai/sessiongraphprotocol v0.0.0
	golang.org/x/net v0.56.0
)

require (
	connectrpc.com/connect v1.20.0 // indirect
	github.com/beorn7/perks v1.0.0 // indirect
	github.com/boltdb/bolt v1.3.1 // indirect
	github.com/cayleygraph/quad v1.1.0 // indirect
	github.com/dennwc/base v1.0.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.0 // indirect
	github.com/golang/snappy v0.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/prometheus/client_golang v0.9.3 // indirect
	github.com/prometheus/client_model v0.0.0-20190129233127-fd36f4220a90 // indirect
	github.com/prometheus/common v0.4.0 // indirect
	github.com/prometheus/procfs v0.0.0-20190507164030-5867b95ac084 // indirect
	github.com/syndtr/goleveldb v1.0.0 // indirect
	github.com/tylertreat/BoomFilters v0.0.0-20181028192813-611b3dbe80e8 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace (
	github.com/restrukt-ai/openagentcontainers => ../../../openagentcontainers
	github.com/restrukt-ai/sessiongraphprotocol => ../..
)
