package import_data

import (
	sp "cloud.google.com/go/spanner"
	"context"
	spannerclient "github.com/GoogleCloudPlatform/spanner-migration-tool/accessors/clients/spanner/client"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/spanner/writer"
	"sync/atomic"
)

func getBatchWriterWithConfig(spannerClient spannerclient.SpannerClient, conv *internal.Conv) *writer.BatchWriter {
	// TODO: review these limits
	config := writer.BatchWriterConfig{
		BytesLimit: 100 * 1000 * 1000,
		WriteLimit: 2000,
		RetryLimit: 1000,
		Verbose:    internal.Verbose(),
	}

	rows := int64(0)
	config.Write = func(m []*sp.Mutation) error {
		ctx := context.Background()
		_, err := spannerClient.Apply(ctx, m)
		if err != nil {
			return err
		}
		atomic.AddInt64(&rows, int64(len(m)))
		return nil
	}
	batchWriter := writer.NewBatchWriter(config)
	conv.SetDataMode()
	conv.SetDataSink(
		func(table string, cols []string, vals []interface{}) {
			batchWriter.AddRow(table, cols, vals)
		})
	conv.DataFlush = func() {
		batchWriter.Flush()
	}
	return batchWriter
}
