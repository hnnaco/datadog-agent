// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package marshal

import (
	"bytes"
	"io"

	model "github.com/DataDog/agent-payload/v5/process"
	"github.com/DataDog/datadog-agent/pkg/network"
	"github.com/DataDog/datadog-agent/pkg/network/protocols/postgres"
	"github.com/DataDog/datadog-agent/pkg/network/types"
)

type postgresEncoder struct {
	postgresAggregationsBuilder *model.DatabaseAggregationsBuilder
	byConnection                *USMConnectionIndex[postgres.Key, *postgres.RequestStat]
}

func newPostgresEncoder(postgresPayloads map[postgres.Key]*postgres.RequestStat) *postgresEncoder {
	if len(postgresPayloads) == 0 {
		return nil
	}

	return &postgresEncoder{
		postgresAggregationsBuilder: model.NewDatabaseAggregationsBuilder(nil),
		byConnection: GroupByConnection("postgres", postgresPayloads, func(key postgres.Key) types.ConnectionKey {
			return key.ConnectionKey
		}),
	}
}

func (e *postgresEncoder) EncodeConnection(c network.ConnectionStats, builder *model.ConnectionBuilder) (uint64, map[string]struct{}) {
	if e == nil {
		return 0, nil
	}

	connectionData := e.byConnection.Find(c)
	if connectionData == nil || len(connectionData.Data) == 0 || connectionData.IsPIDCollision(c) {
		return 0, nil
	}

	staticTags := uint64(0)
	builder.SetDatabaseAggregations(func(b *bytes.Buffer) {
		staticTags |= e.encodeData(connectionData, b)
	})
	return staticTags, nil
}

func (e *postgresEncoder) encodeData(connectionData *USMConnectionData[postgres.Key, *postgres.RequestStat], w io.Writer) uint64 {
	var staticTags uint64
	e.postgresAggregationsBuilder.Reset(w)

	for _, kv := range connectionData.Data {
		key := kv.Key
		stats := kv.Value
		staticTags |= stats.StaticTags
		e.postgresAggregationsBuilder.AddAggregations(func(builder *model.DatabaseStatsBuilder) {
			builder.SetPostgres(func(statsBuilder *model.PostgresStatsBuilder) {
				statsBuilder.SetTableName(key.Parameters)
				statsBuilder.SetOperation(uint64(toPostgresModelOperation(key.Operation)))
				if latencies := stats.Latencies; latencies != nil {
					statsBuilder.SetLatencies(func(b *bytes.Buffer) {
						latencies.EncodeProto(b)
					})
				} else {
					statsBuilder.SetFirstLatencySample(stats.FirstLatencySample)
				}
				statsBuilder.SetCount(uint32(stats.Count))
			})
		})
	}

	return staticTags
}

func (e *postgresEncoder) Close() {
	if e == nil {
		return
	}

	e.byConnection.Close()
}

func toPostgresModelOperation(op postgres.Operation) model.PostgresOperation {
	switch op {
	case postgres.SelectOP:
		return model.PostgresOperation_PostgresSelectOp
	case postgres.InsertOP:
		return model.PostgresOperation_PostgresInsertOp
	case postgres.UpdateOP:
		return model.PostgresOperation_PostgresUpdateOp
	case postgres.CreateTableOP:
		return model.PostgresOperation_PostgresCreateOp
	case postgres.DropTableOP:
		return model.PostgresOperation_PostgresDropOp
	case postgres.DeleteTableOP:
		return model.PostgresOperation_PostgresDeleteOp
	case postgres.AlterTableOP:
		return model.PostgresOperation_PostgresAlterOp
	case postgres.TruncateTableOP:
		return model.PostgresOperation_PostgresTruncateOp
	case postgres.ShowOP:
		return model.PostgresOperation_PostgresShowOp
	default:
		return model.PostgresOperation_PostgresUnknownOp
	}
}
