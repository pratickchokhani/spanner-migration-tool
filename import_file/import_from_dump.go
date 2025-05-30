package import_file

import (
	"bufio"
	"context"
	"fmt"
	spanneraccessor "github.com/GoogleCloudPlatform/spanner-migration-tool/accessors/spanner"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/constants"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/file_reader"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/logger"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/common"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/mysql"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/postgres"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/spanner/writer"
	"go.uber.org/zap"
)

var NewSpannerAccessor = func(ctx context.Context, dbURI string) (spanneraccessor.SpannerAccessor, error) {
	return spanneraccessor.NewSpannerAccessorClientImplWithSpannerClient(ctx, dbURI)
}

type ImportFromDump interface {
	CreateSchema(ctx context.Context, dialect string) (*internal.Conv, error)
	ImportData(ctx context.Context, conv *internal.Conv) error
}

type ImportFromDumpImpl struct {
	ProjectId       string
	InstanceId      string
	DatabaseName    string
	DumpUri         string
	dbUri           string
	dumpReader      file_reader.FileReader
	SourceFormat    string
	SpannerAccessor spanneraccessor.SpannerAccessor
	schemaToSpanner common.SchemaToSpannerInterface
	dbDumpProcessor common.DbDump
}

func NewImportFromDump(
	projectId string,
	instanceId string,
	databaseName string,
	dumpUri string,
	sourceFormat string,
	dbURI string,
	sp spanneraccessor.SpannerAccessor,
	sourceReader file_reader.FileReader) (ImportFromDump, error) {
	dbDump, err := getDbDump(sourceFormat)
	if err != nil {
		return nil, err
	}

	schemaToSpanner := &common.SchemaToSpannerImpl{}

	return &ImportFromDumpImpl{
		projectId,
		instanceId,
		databaseName,
		dumpUri,
		dbURI,
		sourceReader,
		sourceFormat,
		sp,
		schemaToSpanner,
		dbDump,
	}, nil
}

// CreateSchema Process database dump file. Convert schema to spanner DDL. Update the provided database with the schema.
func (source *ImportFromDumpImpl) CreateSchema(ctx context.Context, dialect string) (*internal.Conv, error) {
	reader, err := source.dumpReader.CreateReader(ctx)
	if err != nil {
		logger.Log.Error("Failed to create reader:", zap.Error(err))
		return nil, fmt.Errorf("failed to create reader: %v", err)
	}

	r := internal.NewReader(bufio.NewReader(reader), nil)
	conv := internal.MakeConv()
	conv.SpDialect = dialect
	conv.Source = source.SourceFormat
	conv.SpProjectId = source.ProjectId
	conv.SpInstanceId = source.InstanceId
	conv.SetSchemaMode() // Build schema and ignore data in dump.
	conv.SetDataSink(nil)
	if err := source.dbDumpProcessor.ProcessDump(conv, r); err != nil {
		logger.Log.Error("Failed to parse the dump file:", zap.Error(err))
		return nil, fmt.Errorf("failed to process source schema: %v", err)
	}

	if err := common.ConvertSchemaToSpannerDDL(conv, source.dbDumpProcessor, source.schemaToSpanner); err != nil {
		logger.Log.Error("Failed to convert schema to spanner DDL:", zap.Error(err))
		return nil, fmt.Errorf("failed to convert schema to spanner DDL: %v", err)
	}

	err = source.SpannerAccessor.UpdateDatabase(ctx, source.dbUri, conv, source.SourceFormat)
	if err != nil {
		return nil, fmt.Errorf("can't update database: %v", err)
	}
	source.SpannerAccessor.Refresh(ctx, source.dbUri)

	return conv, nil
}

// ImportData process database dump file. Convert insert statement to spanner mutation. Load data into spanner.
func (source *ImportFromDumpImpl) ImportData(ctx context.Context, conv *internal.Conv) error {
	dumpReader, err := source.dumpReader.ResetReader(ctx)
	if err != nil {
		return fmt.Errorf("can't read dump file: %s due to: %v", source.DumpUri, err)
	}
	logger.Log.Info(fmt.Sprintf("Importing %d rows.", conv.Rows()))
	r := internal.NewReader(bufio.NewReader(dumpReader), nil)
	batchWriter := writer.GetBatchWriterWithConfig(ctx, source.SpannerAccessor.GetSpannerClient(), conv)

	if err := source.dbDumpProcessor.ProcessDump(conv, r); err != nil {
		return err
	}
	batchWriter.Flush()

	return nil
}

func getDbDump(sourceFormat string) (common.DbDump, error) {
	switch sourceFormat {
	case constants.MYSQLDUMP:
		return mysql.DbDumpImpl{}, nil
	case constants.PGDUMP:
		return postgres.DbDumpImpl{}, nil
	default:
		return nil, fmt.Errorf("process dump for sourceFormat %s not supported", sourceFormat)
	}
}
