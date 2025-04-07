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
	"github.com/stretchr/testify/assert"
	"testing"
)

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

func TestMysqlImportArgs(t *testing.T) {
	flagArgs := []string{
		"-instance-id=test-instance",
		"-project=test-project",
		"-db-name=test-db",
		"--source-uri=test-uri",
		"-format=mysqldump",
		"--log-level=INFO",
	}

	expectedValues := ImportDataCmd{
		instanceId:   "test-instance",
		project:      "test-project",
		dbName:       "test-db",
		sourceUri:    "test-uri",
		sourceFormat: "mysqldump",
		logLevel:     "INFO",
	}

	fs := flag.NewFlagSet("testSetFlags", flag.ContinueOnError)
	importDataCmd := ImportDataCmd{}
	importDataCmd.SetFlags(fs)
	err := fs.Parse(flagArgs)
	if err != nil {
		t.Fatalf("Failed to parse flags: %v", err)
	}
	assert.Equal(t, expectedValues, importDataCmd, "TestMysqlImportArgs")
}
