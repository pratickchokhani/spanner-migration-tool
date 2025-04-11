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
	"cloud.google.com/go/spanner"
	"context"
	"flag"
	"fmt"
	spannerclient "github.com/GoogleCloudPlatform/spanner-migration-tool/accessors/clients/spanner/client"
	spanneraccessor "github.com/GoogleCloudPlatform/spanner-migration-tool/accessors/spanner"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/common/constants"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/internal"
	"github.com/GoogleCloudPlatform/spanner-migration-tool/spanner/ddl"
	"github.com/stretchr/testify/assert"
	"os"
	"strings"
	"testing"
	"time"
)

const expectedDDL = "CREATE TABLE cart ( \tuser_id STRING(20) NOT NULL , \tproduct_id STRING(20) NOT NULL , \tquantity INT64, \tlast_modified TIMESTAMP NOT NULL , ) PRIMARY KEY (user_id, product_id);CREATE INDEX idx ON cart (quantity)"

func TestBasicCsvImport(t *testing.T) {
	importDataCmd := ImportDataCmd{}

	fs := flag.NewFlagSet("testSetFlags", flag.ContinueOnError)
	importDataCmd.SetFlags(fs)

	importDataCmd.instanceId = ""
	importDataCmd.dbName = "versionone"
	importDataCmd.tableName = "table2"
	importDataCmd.sourceUri = "../test_data/basic_csv.csv"
	importDataCmd.sourceFormat = "csv"
	importDataCmd.schemaUri = "../test_data/basic_csv_schema.csv"
	importDataCmd.csvLineDelimiter = "\n"
	importDataCmd.csvFieldDelimiter = ","
	importDataCmd.project = ""
	importDataCmd.Execute(context.Background(), fs)
}

func TestImportDataCmd_handleDump(t *testing.T) {
	tests := []struct {
		name                string
		sourceUri           string
		dialect             string
		spannerAccessorMock func(t *testing.T) spanneraccessor.SpannerAccessor
		wantErr             bool
	}{
		{
			name:      "Successful MySQL Dump Import",
			sourceUri: "../test_data/basic_mysql_dump.test.out",
			dialect:   constants.DIALECT_GOOGLESQL,
			spannerAccessorMock: func(t *testing.T) spanneraccessor.SpannerAccessor {
				mock := &spanneraccessor.SpannerAccessorMock{
					CreateOrUpdateDatabaseMock: func(ctx context.Context, dbURI, driver string, conv *internal.Conv, migrationType string) error {

						assert.Equal(t, "projects/test-project/instances/test-instance/databases/test-db", dbURI)
						assert.Equal(t, "mysqldump", driver)
						assert.Equal(t, expectedDDL, fetchDDLString(conv))

						return nil
					},
					GetSpannerClientMock: func() spannerclient.SpannerClient {
						return &spannerclient.SpannerClientMock{
							ApplyMock: func(ctx context.Context, ms []*spanner.Mutation, opts ...spanner.ApplyOption) (commitTimestamp time.Time, err error) {
								assert.Equal(t, 1, len(ms))
								mutationString := fmt.Sprintf("%v", *ms[0])
								assert.True(t, strings.Contains(mutationString, "cart"))
								assert.True(t, strings.Contains(mutationString, "901e-a6cfc2b502dc"))
								return time.Now(), nil
							},
						}
					},
					SetSpannerClientMock: func(spannerClient spannerclient.SpannerClient) {

					},
				}
				return mock
			},
			wantErr: false,
		},
		{
			name:      "Failed CreateOrUpdateDatabase",
			sourceUri: "./testdata/mysqldump.sql",
			dialect:   constants.DIALECT_GOOGLESQL,
			spannerAccessorMock: func(t *testing.T) spanneraccessor.SpannerAccessor {
				mock := &spanneraccessor.SpannerAccessorMock{
					CreateOrUpdateDatabaseMock: func(ctx context.Context, dbURI, driver string, conv *internal.Conv, migrationType string) error {
						return fmt.Errorf("failed to create or update database")
					},
					GetSpannerClientMock: func() spannerclient.SpannerClient {
						return &spannerclient.SpannerClientMock{}
					},
				}
				return mock
			},
			wantErr: true,
		},
		{
			name:      "Failed Dump File Read",
			sourceUri: "./testdata/wrongfile.sql",
			dialect:   constants.DIALECT_GOOGLESQL,
			spannerAccessorMock: func(t *testing.T) spanneraccessor.SpannerAccessor {
				mock := &spanneraccessor.SpannerAccessorMock{
					CreateOrUpdateDatabaseMock: func(ctx context.Context, dbURI, driver string, conv *internal.Conv, migrationType string) error {
						return nil
					},
					GetSpannerClientMock: func() spannerclient.SpannerClient {
						return &spannerclient.SpannerClientMock{}
					},
				}
				return mock
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cmd := &ImportDataCmd{
				project:      "test-project",
				instanceId:   "test-instance",
				dbName:       "test-db",
				sourceUri:    tt.sourceUri,
				sourceFormat: constants.MYSQLDUMP,
			}
			spannerAccessorMock := tt.spannerAccessorMock(t)
			err := cmd.handleDump(ctx, fmt.Sprintf("projects/%s/instances/%s/databases/%s", cmd.project, cmd.instanceId, cmd.dbName), tt.dialect, spannerAccessorMock)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResetReader(t *testing.T) {
	// Create a test file
	tmpfile, err := os.CreateTemp("", "testfile.txt")
	if err != nil {
		t.Fatalf("Failed to create temporary file: %v", err)
	}
	defer os.Remove(tmpfile.Name()) // Clean up after test

	_, err = tmpfile.WriteString("Test content")
	if err != nil {
		t.Fatalf("Failed to write to temporary file: %v", err)
	}
	tmpfile.Close()

	// Open the file
	file, err := os.Open(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to open temporary file: %v", err)
	}
	defer file.Close()

	// Read some bytes to change the offset
	buffer := make([]byte, 4)
	_, err = file.Read(buffer)
	if err != nil {
		t.Fatalf("Failed to read from file: %v", err)
	}

	// Check if offset is changed
	if offset, _ := file.Seek(0, 1); offset != 4 {
		t.Fatalf("Expected offset to be 4, got %d", offset)
	}

	// Reset the reader
	resetFile, err := resetReader(file, tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to reset reader: %v", err)
	}
	defer resetFile.Close()

	// Check if the offset is reset to 0
	if offset, _ := resetFile.Seek(0, 1); offset != 0 {
		t.Fatalf("Expected offset to be 0 after reset, got %d", offset)
	}

}

func fetchDDLString(conv *internal.Conv) string {
	return strings.Replace(strings.Join(
		ddl.GetDDL(
			ddl.Config{Comments: false, ProtectIds: false, Tables: true, ForeignKeys: true, SpDialect: conv.SpDialect, Source: "mysql"},
			conv.SpSchema,
			conv.SpSequences), ";"), "\n", " ", -1)
}
