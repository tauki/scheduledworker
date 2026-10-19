module github.com/tauki/scheduledworker/v2/examples/migration

go 1.25.0

require (
	github.com/tauki/scheduledworker v1.0.0
	github.com/tauki/scheduledworker/v2 v2.0.0
)

replace github.com/tauki/scheduledworker/v2 => ../..
