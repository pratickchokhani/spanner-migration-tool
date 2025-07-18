/* Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.*/

package cmd

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/file_reader"

	spanneraccessor "github.com/GoogleCloudPlatform/spanner-migration-tool/accessors/spanner"

	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/constants"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/import_file"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/logger"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/common"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/csv"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/sources/spanner"
	"github.com/google/subcommands"
	"go.uber.org/zap"
)

type ImportDataCmd struct {
	instance          string
	database          string
	tableName         string
	sourceUri         string
	sourceFormat      string
	schemaUri         string
	csvLineDelimiter  string
	csvFieldDelimiter string
	project           string
	databaseDialect   string
}

func (cmd *ImportDataCmd) SetFlags(set *flag.FlagSet) {
	set.StringVar(&cmd.instance, "instance", "", "Spanner instance Id")
	set.StringVar(&cmd.database, "database", "", "Spanner database name. If one with the specified name does not exist, a new one will be created with the same")
	set.StringVar(&cmd.tableName, "table-name", "", "Spanner table name. Optional. If not specified, source-uri name will be used")
	set.StringVar(&cmd.sourceUri, "source-uri", "", "URI of the file to import")
	set.StringVar(&cmd.sourceFormat, "source-format", "", fmt.Sprintf("Format of the file to import. Valid values {%s, %s, %s}", constants.MYSQLDUMP, constants.PGDUMP, constants.CSV))
	set.StringVar(&cmd.schemaUri, "schema-uri", "", "URI of the file with schema for the csv to import. Only non-optional for csv format.")
	set.StringVar(&cmd.csvLineDelimiter, "csv-line-delimiter", "\n", "Token to be used as line delimiter for csv format. Optional. Defaults to '\\n'. Only used for csv format.")
	set.StringVar(&cmd.csvFieldDelimiter, "csv-field-delimiter", ",", "Token to be used as field delimiter for csv format. Optional. Defaults to ','. Only used for csv format.")
	set.StringVar(&cmd.project, "project", "", "Project id for all resources related to this import. Optional")
	set.StringVar(&cmd.databaseDialect, "database-dialect", constants.DIALECT_GOOGLESQL, fmt.Sprintf("Spanner database dialect. Defaults to %s. Valid values {%s, %s}", constants.DIALECT_GOOGLESQL, constants.DIALECT_GOOGLESQL, constants.DIALECT_POSTGRESQL))
}

func (cmd *ImportDataCmd) Execute(ctx context.Context, f *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	logger.Log.Debug(fmt.Sprintf("instance %s, dbName %s, schemaUri %s\n", cmd.instance, cmd.database, cmd.schemaUri))

	err := validateInputLocal(cmd)
	if err != nil {
		logger.Log.Error(fmt.Sprintf("Input validation failed. Reason %v", err))
		return subcommands.ExitFailure
	}

	dialect := getDialectWithDefaults(cmd.databaseDialect)
	dbURI := getDBUri(cmd.project, cmd.instance, cmd.database)

	spannerAccessor, err := validateSpannerAccessor(ctx, dbURI)
	if err != nil {
		logger.Log.Error(fmt.Sprintf("Input validation failed. Reason %v", err))
		return subcommands.ExitFailure
	}

	err = createDatabase(ctx, dbURI, dialect, spannerAccessor)
	if err != nil {
		logger.Log.Error(fmt.Sprintf("Failed to create database. Reason %v", err))
		return subcommands.ExitFailure
	}

	sourceReader, schemaReader, err := validateUriRemote(ctx, cmd)
	if err != nil {
		logger.Log.Error(fmt.Sprintf("Input validation failed. Reason %v", err))
		return subcommands.ExitFailure
	}

	defer sourceReader.Close()

	switch cmd.sourceFormat {
	case constants.CSV:
		// schemaReader will only be valid if sourceFormat is CSV
		defer schemaReader.Close()
		err := cmd.handleCsv(ctx, dbURI, dialect, spannerAccessor, sourceReader, schemaReader)
		if err != nil {
			logger.Log.Error(fmt.Sprintf("Unable to handle Csv %v", err))
			return subcommands.ExitFailure
		}
		return subcommands.ExitSuccess
	case constants.MYSQLDUMP, constants.PGDUMP:
		err := cmd.handleDatabaseDumpFile(ctx, dbURI, cmd.sourceFormat, dialect, spannerAccessor, sourceReader)
		if err != nil {
			logger.Log.Error(fmt.Sprintf("Unable to handle MYSQL Dump %v. Please reachout to the support team.", err))
			return subcommands.ExitFailure
		}
		return subcommands.ExitSuccess
	default:
		logger.Log.Warn(fmt.Sprintf("format %s not supported yet", cmd.sourceFormat))
	}
	return subcommands.ExitFailure
}

func createDatabase(ctx context.Context, dbURI, targetDialect string, spannerAccessor spanneraccessor.SpannerAccessor) error {
	if exists, _ := spannerAccessor.CheckExistingDb(ctx, dbURI); exists {

		skipDialectValidation := os.Getenv("IMPORT_CMD_SKIP_DIALECT_VALIDATION")

		// Only used for Emulator integration testing.
		// TODO(b/406423609): Remove once Cl for fix within Emulator is release.
		if skipDialectValidation == "true" {
			return nil
		}

		dialect, err := spannerAccessor.GetDatabaseDialect(ctx, dbURI)
		if err != nil {
			return fmt.Errorf("unable to get database dialect %v", err)
		}

		if dialect != targetDialect {
			return fmt.Errorf("database dialect is different for target dialect. Provided dialect: %s, Database dialect: %s", targetDialect, dialect)
		}
		return nil
	}
	return spannerAccessor.CreateEmptyDatabase(ctx, dbURI, targetDialect)
}

// validateSpannerAccessor validate if spanner is accessible by the provided dbURI. Return spannerAccessor, error.
func validateSpannerAccessor(ctx context.Context, dbURI string) (spanneraccessor.SpannerAccessor, error) {
	spannerAccessor, err := import_file.NewSpannerAccessor(ctx, dbURI)
	if err != nil {
		logger.Log.Error(fmt.Sprintf("Unable to instantiate spanner client %v", err))
		return nil, fmt.Errorf("unable to instantiate spanner client %v", err)
	}

	return spannerAccessor, nil
}

// validateUriRemote validate if source URI and schema URI are accessible. Return sourceReader, schemaReader, error.
// If sourceFormat is not CSV, schemaReader will be nil.
func validateUriRemote(ctx context.Context, input *ImportDataCmd) (file_reader.FileReader, file_reader.FileReader, error) {
	sourceReader, err := file_reader.NewFileReader(ctx, input.sourceUri)
	if err != nil {
		return nil, nil, fmt.Errorf("sourceUri:%v not accessible. Please check the input and access permissions and try again", input.sourceUri)
	}

	var schemaReader file_reader.FileReader
	if input.sourceFormat == constants.CSV {
		schemaReader, err = file_reader.NewFileReader(ctx, input.schemaUri)
		if err != nil {
			sourceReader.Close()
			return nil, nil, fmt.Errorf("schemaUri:%v not accessible. Please check the input and access permissions and try again", input.schemaUri)
		}
	}
	return sourceReader, schemaReader, nil
}

func getDialectWithDefaults(dialect string) string {
	dialect = strings.ToLower(dialect)
	switch dialect {
	case constants.DIALECT_GOOGLESQL:
		return dialect
	case constants.DIALECT_POSTGRESQL:
		return dialect
	default:
		logger.Log.Warn(fmt.Sprintf("Dialect passed is %s . Defaulting to %s", dialect, constants.DIALECT_GOOGLESQL))
		return constants.DIALECT_GOOGLESQL
	}
}

/*
1. instance Id is mandatory and accessible
2. database name is mandatory and accessible
3. source uri is mandatory and accessible
4. source format is valid
5. If CSV, schema URI is mandatory and accessible
*/
func validateInputLocal(input *ImportDataCmd) error {

	var err error
	if len(input.instance) == 0 {
		return fmt.Errorf("Please specify instance using the --instance parameter. Received instance: %v", input.instance)
	}

	if len(input.database) == 0 {
		return fmt.Errorf("Please specify databaseName using the --database parameter. Received  databaseName: %v", input.database)
	}

	if len(input.sourceUri) == 0 {
		return fmt.Errorf("Please specify sourceUri using the --source-uri parameter. Received  sourceUri: %v", input.sourceUri)
	}

	if len(input.sourceFormat) == 0 {
		return fmt.Errorf("Please specify sourceFormat using the --source-format parameter. Received  sourceFormat: %v", input.sourceFormat)
	}

	if input.sourceFormat == constants.CSV && len(input.schemaUri) == 0 {
		return fmt.Errorf("Please specify schemaUri using the --schema-uri parameter. Received  schemaUri: %v", input.sourceFormat)
	}

	return err
}

func (cmd *ImportDataCmd) handleCsv(ctx context.Context, dbURI, dialect string,
	sp spanneraccessor.SpannerAccessor, sourceReader file_reader.FileReader, schemaReader file_reader.FileReader) error {

	cmd.tableName = handleTableNameDefaults(cmd.tableName, cmd.sourceUri)

	infoSchema, err := spanner.NewInfoSchemaImplWithSpannerClient(ctx, dbURI, dialect)
	if err != nil {
		logger.Log.Error(fmt.Sprintf("Unable to instantiate spanner client %v", err))
		return err
	}

	startTime := time.Now()
	csvSchema := import_file.NewCsvSchema(cmd.project, cmd.instance,
		cmd.database, cmd.tableName, cmd.schemaUri, schemaReader)
	err = csvSchema.CreateSchema(ctx, dialect, sp)

	endTime1 := time.Now()
	elapsedTime := endTime1.Sub(startTime)
	logger.Log.Info(fmt.Sprintf("Schema creation took %f secs", elapsedTime.Seconds()))
	if err != nil {
		return err
	}

	csvData := import_file.NewCsvData(cmd.project, cmd.instance,
		cmd.database, cmd.tableName, cmd.sourceUri, cmd.csvFieldDelimiter, sourceReader)
	err = csvData.ImportData(ctx, infoSchema, dialect, internal.MakeConv(), &common.InfoSchemaImpl{}, &csv.CsvImpl{})

	endTime2 := time.Now()
	elapsedTime = endTime2.Sub(endTime1)
	logger.Log.Info(fmt.Sprintf("Data import took %f secs", elapsedTime.Seconds()))
	return err

}

func getDBUri(projectId, instanceId, databaseName string) string {
	return fmt.Sprintf("projects/%s/instances/%s/databases/%s", projectId, instanceId, databaseName)
}

/*
Handle table name defaults, if they are not passed. Assumes sourceUri file name as table name
This method does not handle validation. It is supposed to be called only after calling validateInputLocal method
*/
func handleTableNameDefaults(tableName, sourceUri string) string {
	if len(tableName) != 0 {
		return sanitizeTableName(tableName)
	}

	parsedURL, _ := url.Parse(sourceUri)
	path := parsedURL.Path

	if strings.HasPrefix(path, "/") && len(path) > 1 {
		path = path[1:] // Remove leading slash if present
	}
	basePath := filepath.Base(path)

	// pick the substring before the first dot
	return sanitizeTableName(strings.Split(basePath, ".")[0])

}

func sanitizeTableName(tableName string) string {
	tableName = strings.ToLower(tableName)
	underscoreOrAlphabet := func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '_')
	}

	underscoreOrAlphanumeric := func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			return r
		}
		return -1
	}

	trimmedTableName := strings.TrimLeftFunc(tableName, underscoreOrAlphabet)
	return strings.Map(underscoreOrAlphanumeric, trimmedTableName)
}

func init() {
	logger.Log, _ = zap.NewDevelopment()
}

func (cmd *ImportDataCmd) Name() string {
	return "import"
}

// Synopsis returns summary of operation.
func (cmd *ImportDataCmd) Synopsis() string {
	return "Import data from supported source files to spanner"
}

// Usage returns usage info of the command.
func (cmd *ImportDataCmd) Usage() string {
	return fmt.Sprintf(`%v import --instance-id=i1 --database-name=db1 --source-format=csv --source-uri=uri1 --schema-uri=uri2 ...

Import data from supported source files to spanner
`, path.Base(os.Args[0]))

}

func (cmd *ImportDataCmd) handleDatabaseDumpFile(ctx context.Context, dbUri, sourceFormat string, dialect string,
	sp spanneraccessor.SpannerAccessor, sourceReader file_reader.FileReader) error {

	importDump, err := import_file.NewImportFromDump(cmd.project, cmd.instance, cmd.database, cmd.sourceUri,
		sourceFormat, dbUri, sp, sourceReader)
	if err != nil {
		return fmt.Errorf("can't open dump file or create spanner client: %v", err)
	}

	schemaStartTime := time.Now()
	conv, err := importDump.CreateSchema(ctx, dialect)
	if err != nil {
		return fmt.Errorf("can't create schema: %v", err)
	}

	schemaEndTime := time.Now()
	elapsedTime := schemaEndTime.Sub(schemaStartTime)
	logger.Log.Info(fmt.Sprintf("Schema creation took %f secs", elapsedTime.Seconds()))

	err = importDump.ImportData(ctx, conv)

	dataEndTime := time.Now()
	elapsedTime = dataEndTime.Sub(schemaEndTime)
	logger.Log.Info(fmt.Sprintf("Data import took %f secs", elapsedTime.Seconds()))

	if err != nil {
		return fmt.Errorf("can't import data: %v", err)
	}
	return nil
}
